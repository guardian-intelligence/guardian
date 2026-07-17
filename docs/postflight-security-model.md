# Postflight workload security model

Status: living security policy, 2026-07-17. Defines the customer-facing
security claims for Postflight CI, the threat model behind them, and the
evidence required before a claim ships.

## Positioning

Postflight does not sell regulated confidential computing. Customers with a
zero-trust or FedRAMP-class TEE requirement are served by hyperscalers and
DPU-attested platforms, and no CI runner vendor beats that stack on paper.

What Postflight's customers actually need, and what no fast-runner competitor
offers, is hardware-enforced job isolation plus at-rest protection for
whatever their jobs persist:

1. Every job executes inside its own AMD SEV-SNP virtual machine with
   encrypted memory, created for that job and destroyed after it.
2. Everything the platform persists from a job — the `$GITHUB_WORKSPACE`
   generation, dependency caches, process-memory warm-start images — is
   ciphertext under a key derived inside the CPU's security processor. The
   key is bound to the launch measurement of the published guest image and
   never exists on any host disk, in any database, or on any network path.
3. A stolen disk, leaked ZFS snapshot, exfiltrated backup, or compromised
   storage/backup plane yields ciphertext that cannot be decrypted off-chip.
   A secret accidentally persisted in a workspace is covered by the same
   property.
4. Each job's SNP attestation report is retained as evidence, and golden
   image launch measurements are published per release, so the SNP claim is
   verifiable rather than asserted.

## Threat model

### Adversaries in scope

- **Malicious tenant job.** Every CI job may run a hostile guest kernel with
  an undisclosed guest-to-host or guest-kernel exploit. It must not escape
  the VM, reach another tenant's data, reach management or control planes,
  derive another scope's volume key, or leave state for the next job.
- **Data-at-rest attacker.** Anyone who obtains worker disks, zvols, ZFS
  snapshots, replicated backups, or decommissioned media — including a
  compromise of the storage or backup plane itself — obtains only
  ciphertext.
- **Network attacker.** On-path adversaries between guest, worker, control
  plane, and GitHub cannot read job secrets or forge lifecycle decisions.
- **Residual-state attacker.** No customer plaintext, key material, or JIT
  credential survives VM destruction in a host-accessible form.

### Trusted

- The AMD PSP, SEV-SNP firmware, and endorsement root, under an active
  security-bulletin policy.
- The Guardian control plane, worker hypervisor stack (host kernel, KVM,
  QEMU, hostd), and the golden-image build pipeline. Workers are hardened
  and audited, but they are inside the trust boundary.
- GitHub, for everything GitHub already sees by construction.

### Explicit non-claims

- **No operator-proof confidentiality.** A malicious Guardian operator with
  live hypervisor control is outside this model. SNP memory encryption
  raises the cost of casual host-side inspection, but Postflight does not
  claim its own infrastructure is its adversary.
- **No cache freshness guarantee.** Warm state is a performance artifact. A
  rolled-back or discarded generation produces a cold build, never a
  confidentiality loss.
- No side-channel guarantees beyond the documented SMT policy, no
  availability guarantees against worker failure, and no protection for
  customer code that exfiltrates its own secrets.

## Architecture

### Execution

One job, one VM, destroy-and-refill; VMs are claimed from a pre-booted
SEV-SNP warm pool. Guest policy prohibits debug access and migration agents.
Customer-bearing vmstate (whole-VM RAM snapshots) is prohibited — SNP forbids
it and the warm pool never needs it.

SMT is enabled for density, with sibling hyperthreads gang-scheduled to the
same VM so no physical core is ever shared across tenants, and the SNP guest
policy admits SMT accordingly.

### Measured boot

Stateless OVMF, QEMU direct kernel/initramfs boot with `kernel-hashes=on`,
the dm-verity root hash on the measured command line, root mounted
read-only, no writable NVRAM. Measured boot is load-bearing for the at-rest
claim: it is what binds the derived key to the reviewed guest image instead
of to any guest that boots on the chip.

### At-rest encryption

Implemented in `src/services/postflight/guestd/volkey.go`:

- Workspace, cache, and warm-start zvols are LUKS2 (aes-xts) formatted and
  opened inside the guest before the mount ladder runs.
- The volume key is `HKDF(SNP_GET_DERIVED_KEY, info)` with the PSP key
  rooted in VCEK and field-selecting `MEASUREMENT | GUEST_POLICY`. Only a
  same-measurement, same-policy guest on the same chip can re-derive it.
- The encryption mode file is baked into the golden image and never
  host-supplied; unknown modes fail closed; a plaintext device presented
  under an encryption mode is refused. The `dev-insecure` mode must never
  appear in a production image.
- `/dev/sev-guest` is root-only inside the guest; runner-user job processes
  cannot invoke the derived-key ioctl.
- The JIT runner configuration exists only in guest RAM and the runner's
  process environment, never on any disk.

### Warm-start (CRIU) rules

Process-memory warm-start images are the hottest plaintext the platform
handles, so they carry standing rules:

- A dump is written only into an already-opened encrypted volume, never
  staged on plaintext.
- No live credential, `GITHUB_TOKEN`, or runner-registration state may be
  present in a dump; runners register fresh per job.
- Restore happens in a fresh VM before any customer-controlled code runs.

### Consequences accepted by design

- **Chip binding means host-affinity warmth.** Warm state restores only on
  the worker that produced it, which the ZFS substrate already requires; a
  job scheduled elsewhere cold-builds.
- **An image roll is a fleet-wide cold build.** A new golden image has a new
  measurement, therefore a new derived key, therefore no access to prior
  generations. Rolls are staggered; caches are scoped per image by
  construction.
- **Disclosed metadata.** The worker sees ciphertext sizes, TRIM patterns
  (`--allow-discards` is enabled for sparse-zvol accounting), I/O volume,
  and job timing.

### Guest–host surface

The only guest↔host channel is the closed `guestproto` vsock protocol. No
QEMU guest agent, no host-commandable exec or file interfaces, no serial
shell. Sandbox isolation doctrine — the VMM jail, per-VM network namespace
and default-deny egress, cgroup containment, and the no-cross-tenant-KSM
rule — is defined in `postflight-product.md` §16 and applies to every
workload node.

## Evidence

- Each job record retains the guest's SNP attestation report (measurement,
  policy, signed by the VCEK chain).
- Golden-image launch measurements are published per release alongside the
  image provenance already required by the supply-chain design.
- A customer or auditor can verify any job's report against the AMD root
  and the published measurement without Guardian's cooperation. Guardian
  runs no verifier service and holds no key-release authority; there is no
  key-distribution plane to audit.

## Release gates

Every gate runs on the exact hardware, firmware, guest image, and QEMU tuple
admitted to production. No customer job runs on a tuple with a failing gate.

| ID | Gate | Pass condition |
| --- | --- | --- |
| G1 | SNP launch | Guests launch with SNP active, debug and migration prohibited by policy, and the expected measurement; a mutated kernel, initramfs, command line, or dm-verity root changes the measurement. |
| G2 | Attestation evidence | Every job produces a VCEK-verifiable report matching the published measurement; a non-SNP or wrong-image guest is refused a runner assignment. |
| G3 | Off-chip decryption fails | A workspace zvol copied off the worker cannot be opened with any key derivable off that chip; a different-measurement guest on the same chip fails to open it. |
| G4 | No production plaintext mode | The production golden image bakes `snp` mode; `dev-insecure` and `off` fail image admission. |
| G5 | Plaintext marker scan | High-entropy markers written inside a job are absent from raw zvols, snapshots, backups, host logs, and host page cache — with a positive control proving each detector trips. |
| G6 | Warm-start image scan | A CRIU dump on host storage is ciphertext; structural decode (`crit`) of the raw device fails; no credential or registration material exists in any dump taken from a compliant guest. |
| G7 | JIT credential residency | JIT config is absent from host disks, argv, environment, logs, and crash artifacts. |
| G8 | Lifecycle destruction | Success, failure, cancellation, timeout, and crash paths all destroy the VM and its slot state; a residue scan finds no leftover mapper devices, keys, or mounts; no VM accepts a second job. |
| G9 | Derived-key device permission | Runner-user processes inside the guest cannot open `/dev/sev-guest`. |
| G10 | Isolation battery | The §16 jail conformance suite passes: non-root QEMU, seccomp/no-new-privs, per-VM MAC label, netns default-deny, cgroup caps, no cross-tenant sibling scheduling. |
| G11 | Golden image customer-free | Image scans find no prior customer data, credential, or host identity. |
| G12 | Bulletin currency | Admitted TCB versions meet minimums derived from active AMD bulletins; a newly prohibited TCB removes the tuple from scheduling. |

## Future hardening

Each row is adopted only on customer or regulatory pull, as a separately
reviewed change to this model.

| Extension | Adopt when |
| --- | --- |
| Attestation-gated key release through an independent verifier (RATS-style), moving key authority off the trusted-operator assumption | A customer segment requires operator-independent key custody and will pay for its latency and operational cost. |
| Customer-held or customer-wrapped workspace keys | Same trigger, strictest form; requires a customer-side verification flow. |
| Rollback-freshness anchoring for generations (remote manifest, compare-and-swap promotion) | Cache integrity becomes a customer-facing claim rather than a performance property. |
| Alternate VMM substrate (`microvm`, Cloud Hypervisor) | The pinned QEMU/q35 tuple is stable in production and a measured comparison of device surface, boot latency, and escape posture justifies migration. |

# Postflight workload security model

Status: living security policy, 2026-07-24. Defines the customer-facing
security claims for both Postflight products, the threat model behind each,
and the evidence required before a claim ships.

## Positioning

Postflight sells two products with two honest security postures:

- **Lightning** — the fastest CI money can buy, with hardware VM isolation
  and at-rest encryption. Its host is trusted infrastructure, and we say so.
- **Confidential** — the same speed architecture inside SEV-SNP, where a
  compromised worker host reads nothing. Customers trust Guardian and AMD —
  not our hosting providers, and not any single machine.

Wording discipline: claims say "verifiable" and "by construction", never
"provably". Any capability originating on our side of the GitHub App is
unfalsifiable to the customer (an App owner can always mint keys), so
operator-exclusion is not claimed by either product; it is the Black tier's
territory (see adopt-on-pull).

## Claims

### Confidential

1. Every job executes in its own SEV-SNP virtual machine with encrypted,
   integrity-protected memory, created for that job and destroyed after it.
2. **A compromised worker host cannot read or alter job data through any
   designed interface.** The host launches guests, moves ciphertext, and
   relays sealed frames it cannot open. This includes the hosting provider's
   personnel, BMC, and firmware footholds.
3. Everything persisted from a job — workspace, tool caches, process-memory
   capsules — is ciphertext under keys derived inside the CPU's security
   processor, mixed with a Guardian-held tenant key. Volume keys exist only
   in guest RAM: never on a host disk, in a database, on a network path, or
   in Guardian's hands.
4. No credential is released to a guest before its SNP attestation is
   verified. Each job's attestation report is retained as evidence, and
   golden-image launch measurements are published per release, so every claim
   above is checkable by the customer without Guardian's cooperation.

### Lightning

1. Every job executes in its own hardware virtual machine (KVM), created for
   that job and destroyed after it. One physical core never serves two
   tenants.
2. Everything persisted from a job is ciphertext under per-lineage keys
   custodied in Guardian's OpenBao Transit. A stolen disk, leaked snapshot,
   or compromised storage plane yields ciphertext.
3. **Explicit boundary:** Lightning's hosts are trusted Guardian
   infrastructure. A live compromise of a worker host could expose the jobs
   on it. Customers who need that boundary closed buy Confidential — the
   distinction is the product line, not fine print.

## Threat model

### Adversaries in scope

| Adversary | Lightning | Confidential |
| --- | --- | --- |
| Malicious tenant job (hostile guest kernel, escape attempts, residue) | ✔ | ✔ |
| Data-at-rest attacker (disks, zvols, snapshots, decommissioned media, storage plane) | ✔ | ✔ |
| Network attacker (on-path between guest, host, control plane, GitHub) | ✔ | ✔ |
| Residual-state attacker (plaintext or credentials surviving VM destruction) | ✔ | ✔ |
| **Byzantine host** (compromised host kernel/KVM/QEMU/hostd, rogue provider personnel, BMC access) | ✖ trusted | ✔ |

### Trusted

- Both products: the Guardian control plane, OpenBao, the golden-image build
  pipeline, and GitHub (for everything GitHub already sees by construction).
- Confidential: the AMD PSP, SEV-SNP firmware, and endorsement root under an
  active security-bulletin policy; the published, measured guest image.
  **Not** the worker host stack — kernel, KVM, QEMU, and hostd are operators
  of ciphertext and sealed frames.
- Lightning: additionally the worker host stack, hardened per the sandbox
  isolation doctrine ([product doc §16](postflight-product.md)).

### Explicit non-claims

- **No operator-proof confidentiality.** A malicious Guardian — control
  plane, App key, or image pipeline — is outside both models. The guest
  image is public and measured precisely so this trust is inspectable, but it
  is trust.
- **No side-channel guarantees beyond policy.** SNP narrows host-side
  inspection to the published side-channel class; we track AMD bulletins,
  enforce TCB floors, and gang-schedule SMT siblings, and we do not claim
  more than that.
- **No cache freshness beyond the rollback floor.** A discarded or
  rolled-back generation produces a cold build, never a confidentiality
  loss.
- No availability guarantees against host failure, and no protection for
  customer code that exfiltrates its own secrets.

## Architecture — Confidential

### Measured boot

Stateless OVMF, QEMU direct kernel/initramfs boot with `kernel-hashes=on`,
the dm-verity root hash on the measured command line, root mounted read-only,
no writable NVRAM. The launch measurement binds every downstream key and
credential to the reviewed, published guest image — not to whatever boots on
the chip.

### Attested sessions: the host is a conduit

At pool boot, before any customer demand exists:

1. guestd generates an ephemeral keypair and requests an SNP report with
   `report_data = H(public key ‖ control-plane nonce)`.
2. The control plane verifies the report: VCEK chain to the AMD root, launch
   measurement against the published set, TCB at or above the class's
   bulletin-derived floor, policy bits (debug and migration prohibited).
3. On success it establishes a **sealed session** with that guest, keyed to
   the attested ephemeral key and bound to the pool-member incarnation.

Everything secret-bearing crosses only the sealed session, relayed by hostd
as opaque frames: the JIT runner configuration, the tenant key half, and any
future material. hostd holds no decrypt capability by construction — there
is no code path in which a credential exists in host memory as plaintext.
Guest-originated lifecycle reports (assignment observation, restore results,
seal evidence) are authenticated under the same session, so a byzantine host
can delay or drop guest messages but cannot forge them.

A guest that fails verification gets nothing: no JIT, no keys, no listener.
It is destroyed and the failure is evidence.

### At-rest keys

Implemented in guestd; the invariant set:

- Volume keys are derived **in-guest**:
  `K_volume = HKDF(SNP_GET_DERIVED_KEY ‖ K_tenant, salt = tenant ‖ volume ‖ generation)`
  with the PSP key rooted in VCEK and field-selecting
  `MEASUREMENT | GUEST_POLICY`. The measurement must be a derivation input —
  host-supplied fields alone could be replayed into a malicious guest.
- `K_tenant` is the Guardian-held half: a per-tenant key custodied in
  `transit-postflight`, released only over the sealed session, zeroized in
  the guest before customer code runs. Both halves are required, so
  "Guardian cannot read your caches" is literally true (we lack the chip
  half) and cross-tenant separation on shared silicon does not lean on the
  scheduler alone. The guest cross-checks the volume salt's tenant against
  the job's tenant and fails closed on mismatch.
- **Volume keys never cross the guest boundary in either direction.** The
  host injects none; the guest exports none. Verification probes compare
  HMAC fingerprints, never key bytes.
- Chip binding makes warmth host-affine: a capsule restores only on the
  chip that produced it, an image roll re-keys the fleet, and both cost cold
  builds — accepted, because warm state is a regenerable cache.
- The encryption mode is baked into the measured image and never
  host-supplied; unknown modes fail closed; a plaintext device presented
  under an encryption mode is discarded and reformatted. `/dev/sev-guest` is
  root-only in-guest, out of reach of runner-user job processes.

### Freshness

Generation manifests are signed (see Transit) and carry a monotonic
generation number; the control-plane catalog owns the current pointer and the
rollback floor. The rendezvous message delivers the manifest and floor over
the sealed session, so a byzantine host cannot present a stale-but-authentic
generation: the guest refuses anything below the floor. Integrity without
freshness is not enough, and the floor's authority is deliberately off-host.

### CRIU rules

Process-memory capsules are the hottest plaintext the platform handles:

- A dump is written only into an already-opened encrypted volume, never
  staged on plaintext.
- No live credential, `GITHUB_TOKEN`, or runner-registration state may exist
  in a dump; the runner processes are killed and proven absent before the
  capsule freezes. Runners register fresh per job.
- Restore happens in a fresh attested VM before any customer-controlled code
  runs.

## Architecture — Lightning

Same guest image family, same mount ladder, same CRIU rules — different key
custody:

- Each volume lineage gets a random DEK generated as a `transit-postflight`
  data key. The wrapped form lives in the generation catalog; the plaintext
  half is delivered to guestd at rendezvous over the authenticated control
  channel and exists only in guest RAM.
- Claims follow custody: this protects data at rest against disk theft,
  snapshot leaks, and storage-plane compromise. It does not protect against
  a live host compromise, and the model says so (claim L3).
- Deleting a tenant's Transit key is crypto-erase for everything it wraps.
- Lightning classes may expose `/dev/kvm`; the host jail doctrine
  ([product doc §16](postflight-product.md)) applies in full.

## OpenBao Transit, product-scoped

`transit-postflight` is a dedicated Transit mount owned by the Postflight
product, provisioned per the [OpenBao design](openbao-design.md) (self-init
declares the mount; durable keys are data):

| Key | Purpose | Operations |
| --- | --- | --- |
| `tenant-<id>` | Confidential `K_tenant` custody; Lightning lineage DEK wrapping | datakey generate, decrypt |
| `postflight-manifest` | Generation-manifest signing | sign, verify |

Rules: keys are non-exportable and deny-by-default; each control-plane module
gets the minimum verb set (metering can verify, only the session module can
decrypt); every operation is audit-shipped; key deletion is the tenant
crypto-erase mechanism and requires the same ceremony as any custody-tier
destruction. DR follows the OpenBao restore-not-reseed doctrine — and the
tested-restore drill is a **release gate** here, because Transit now protects
durable customer ciphertext: no Lightning at-rest claim ships before a
cold-start restore has decrypted pre-restore ciphertext.

Rotation creates a new lineage (one cold build); it never rewrites volumes.

## Evidence

- Per job: the guest's SNP attestation report (Confidential), the runner and
  image versions, and the generation manifest chain.
- Per release: published golden-image launch measurements and the pinned
  QEMU artifact digest, alongside the supply-chain provenance the release
  lane already requires.
- A customer or auditor verifies any job's report against the AMD root and
  the published measurement without Guardian's cooperation. Guardian's own
  verifier gates credential release, but no claim depends on trusting it —
  the same report is theirs to check.

## Release gates

Every gate runs on the exact hardware, firmware, guest image, and QEMU tuple
admitted to production. No customer job runs on a tuple with a failing gate.
Each gate needs a positive control proving the detector trips.

| ID | Product | Gate | Pass condition |
| --- | --- | --- | --- |
| G1 | C | SNP launch | Guests launch with SNP active, debug/migration prohibited, expected measurement; any mutated boot input changes the measurement. |
| G2 | C | Attestation-gated release | A guest with a wrong measurement, stale TCB, or bad VCEK chain receives no JIT, no `K_tenant`, no listener. |
| G3 | C | Conduit host | JIT and tenant-key material never appear as plaintext in host memory dumps, disks, argv, environment, or logs; wire frames are ciphertext; hostd binaries contain no unseal path. |
| G4 | C | Off-chip decryption fails | A volume copied off the worker cannot be opened with any key derivable off that chip; a different-measurement guest on the same chip fails to open it. |
| G5 | C | Forged-message rejection | Guest lifecycle messages replayed or forged by the host are rejected by the control plane; assignment bindings require the sealed session. |
| G6 | C | Rollback refusal | A stale-but-authentic generation presented below the floor is refused in-guest; the job runs cold. |
| G7 | both | Plaintext marker scan | High-entropy markers written inside a job are absent from raw zvols, snapshots, host logs, and host page cache. |
| G8 | both | Capsule scan | A CRIU dump on host storage is ciphertext; structural decode fails; no credential or registration material exists in any dump from a compliant guest. |
| G9 | both | Lifecycle destruction | Success, failure, cancellation, timeout, and crash paths all destroy the VM and slot state; residue scans find no mapper devices, keys, or mounts; no VM accepts a second job. |
| G10 | both | Isolation battery | The §16 jail conformance suite passes: non-root QEMU, seccomp, per-VM network isolation, cgroup caps, no cross-tenant sibling scheduling. |
| G11 | both | Image hygiene | Golden images are customer-free and, on Confidential, bake the production encryption mode; insecure modes fail image admission. |
| G12 | C | Bulletin currency | Admitted TCB versions meet minimums from active AMD bulletins; a newly prohibited TCB removes the tuple from scheduling. |
| G13 | L | Transit custody | Lightning DEKs exist on no host disk or log; wrapped DEKs are useless without Transit; deleting a tenant key renders its lineages undecryptable (verified). |
| G14 | L | Transit restore drill | A cold-start OpenBao restore decrypts pre-restore Lightning ciphertext before any at-rest claim ships. |

## Adopt on pull

Each row is adopted only on customer or regulatory pull, as a separately
reviewed change to this model:

| Extension | Adopt when |
| --- | --- |
| Operator-independent key custody: customer-held or customer-wrapped keys, external RATS-style verifier | An enterprise segment requires it and pays for its latency and ceremony — this is the Black tier's defining feature, not a Confidential upgrade. |
| Portable warmth: attested guest-to-guest key handoff so capsules survive host loss and image rolls | Fleet scale makes cold-on-miss measurably expensive; requires its own reviewed key-release design. |
| Side-channel hardening beyond bulletin policy | A customer threat model names a specific vector with evidence. |

Related: [architecture](postflight-architecture.md) ·
[fleet](postflight-fleet.md) · [storage](postflight-storage.md) ·
[host](postflight-host.md) · [OpenBao design](openbao-design.md)

# Postflight confidential-computing security validation

Status: proposed living security policy with resolved architecture decisions,
2026-07-16. These decisions are design inputs, not claims that the system is
implemented or has passed the release gates below.

## Purpose

This document defines the evidence Postflight must produce before claiming that
customer CI workspaces are confidential from a compromised worker node. It is a
security validation program, not only a penetration test: no finite pentest
proves a system secure, and many of the most important properties are better
checked by conformance tests, fault injection, fuzzing, model checking, and
continuous canaries.

Customer promise: Only an approved, freshly attested guest with an active
control-plane lease can obtain the workspace key authorized for one generation.
A worker node cannot read customer plaintext or cause an altered, substituted,
forked, or rolled-back generation to be accepted without detection.

A worker can deny service, delay or drop I/O, observe disclosed metadata, and
attempt side channels outside the guaranteed AMD SEV-SNP threat model.

No external customer secret may enter this path until the release-blocking tests in this document pass on the exact hardware, firmware, guest image, VMM, attestation policy, and key-release policy admitted to production.

## Upstream guidance

There is no Confidential Computing Consortium (CCC) turnkey penetration-test
standard for this architecture. The useful upstream material divides into:

- The CCC [technical analysis and threat
  model](https://confidentialcomputing.io/wp-content/uploads/sites/10/2023/03/CCC-A-Technical-Analysis-of-Confidential-Computing-v1.3_unlocked.pdf)
  treats a hostile host, attestation protocols, external storage, rollback, and
  replay as system-design concerns. It explicitly excludes availability and
  sophisticated invasive CPU attacks from the generic TEE guarantee.
- The CCC [Verifier Governance
  guidance](https://github.com/confidential-computing/governance/blob/main/SIGs/GRC/publications/Verifier_Governance.md)
  treats the verifier as a root of trust comparable in consequence to a CA or
  HSM, requiring current policies, protected keys, tamper-evident histories,
  and tenant isolation.
- [RFC 9334](https://www.rfc-editor.org/rfc/rfc9334.html) supplies the RATS
  roles and vocabulary: Attester, Evidence, Verifier, Endorsements, Reference
  Values, Attestation Result, and Relying Party.
- AMD's [SEV-SNP firmware
  ABI](https://docs.amd.com/v/u/en-US/56860_PUB_1.58_SEV_SNP) and
  [attestation
  guide](https://www.amd.com/content/dam/amd/en/documents/developer/lss-snp-attestation.pdf)
  define the actual report, certificate, TCB, policy, measurement, VMPL, and
  `REPORT_DATA` semantics that the verifier must test.
- Linux's [confidential-guest threat
  model](https://docs.kernel.org/security/snp-tdx-threat-model.html) and
  QEMU's [SEV-SNP launch
  documentation](https://www.qemu.org/docs/master/system/i386/amd-memory-encryption.html)
  define important host-controlled interfaces and the measured launch path.
- Confidential Containers
  [Trustee](https://confidentialcontainers.org/docs/attestation/architecture/)
  is the most directly reusable reference architecture. It separates hardware
  evidence verification, reference-value appraisal, and resource-release
  authorization, and binds its secure channel to fresh attestation.

## Resolved architecture

The first production design has no selectable security backends. A change to
any row in this table is a material architecture change that reopens the threat
model, reference measurements, and applicable release tests.

| Boundary | Selected design |
| --- | --- |
| Guest privilege model | Direct AMD SEV-SNP Linux guest at VMPL0; no SVSM and no vTPM. |
| Boot chain | Stateless measured OVMF; QEMU direct kernel/initramfs boot with `kernel-hashes=on`; immutable guest root mounted read-only through dm-verity with its root hash in the measured command line. No writable NVRAM. |
| VMM profile | QEMU/KVM using the existing pinned `pc-q35-8.2` machine ABI and the minimum reviewed virtio device set. A different machine ABI or VMM requires a new evidence tuple. |
| SNP policy | Debug, migration agents, and SMT are prohibited. The guest runs on one socket. Customer-bearing vmstate, save/restore, and live migration are prohibited. |
| Endorsement identity | AMD VCEK with unmasked chip ID. Only enumerated processor families, report versions, and minimum TCBs are admitted. VLEK is not accepted. |
| Attestation and release | Confidential Containers [Trustee v0.19.0](https://github.com/confidential-containers/trustee/releases/tag/v0.19.0) KBS, Attestation Service, and RVPS run as separate, network-isolated management-cluster services. Production uses authenticated TLS, non-sample signed reference values, default-deny attestation and resource policies, and separately authorized administration. |
| Key custody | Trustee's [OpenBao KV backend](https://github.com/confidential-containers/trustee/blob/v0.19.0/kbs/docs/vault_kv.md) reads workspace resources from a dedicated OpenBao KV v1 mount over verified TLS. The KBS identity is read-only; a separate control-plane identity creates, rotates, and destroys keys. |
| Persistent workspace | [OpenZFS v2.4.3](https://github.com/openzfs/zfs/releases/tag/zfs-2.4.3) is the initial review baseline. A single-device pool runs inside the guest on the raw zvol exposed by the worker. The workspace encryption root uses `encryption=aes-256-gcm`, `keyformat=raw`, `keylocation=prompt`, `checksum=sha256`, `compression=off`, and `dedup=off`; autotrim/discard is disabled. Pool and dataset names are constant and contain no customer identity. |
| Key granularity | One random 256-bit OpenZFS lineage wrapping key per tenant and workspace lineage, never per generation. A lineage never crosses a tenant or repository. A PR/fork-like scope may descend only when it is already authorized to read the source generation, and it never gains trusted-branch promotion rights. Every generation is separately authorized, but CoW-related generations in one lineage deliberately share the wrapping key. A trust-boundary change or cryptographic rekey creates a new encryption root and performs a full copy. |
| Freshness | A remote authoritative manifest and compare-and-swap operation own the current generation. An encrypted deterministic-CBOR marker inside the guest dataset binds lineage, generation, parent, and seal nonce. The remote manifest stores its digest. |
| Assignment authority | Before launch, the control plane issues a short-lived Ed25519 JWS launch authorization with fixed `EdDSA` algorithm, audience, lease ID, unique `jti`, tenant, scope, lineage, generation, parent, marker digest, resource URI, and expiry. Its digest is bound using Trustee's [init-data construction](https://github.com/confidential-containers/trustee/blob/v0.19.0/kbs/docs/initdata.md) through SNP `HOST_DATA`. Trustee binds its fresh challenge and the guest ephemeral JWK through `REPORT_DATA`. The control plane accepts one resulting attestation token for the lease and encrypts the JIT configuration to that token's TEE public key. Hostd only relays opaque traffic. |

The boot and restore path is therefore:

1. QEMU launches the direct SNP guest with stateless OVMF, measured
   kernel/initramfs/command line, migration and debug disabled, and SMT off.
2. The initramfs verifies and mounts the immutable dm-verity root. It verifies
   the signed launch authorization whose digest is in `HOST_DATA`, generates an
   ephemeral session key, and obtains a fresh SNP report binding Trustee's
   challenge and that public key through `REPORT_DATA`.
3. Trustee appraises the VCEK chain, TCB, launch measurement, SNP policy,
   reference values, signed init data, expiry, and exact resource context. The
   control plane consumes the lease `jti`, accepts the resulting attestation
   token once, and encrypts the JIT assignment to its TEE public key.
4. After accepting that assignment, measured guestd requests the exact resource.
   Trustee's default-deny policy releases the lineage wrapping key from OpenBao
   only when the attestation, launch authorization, and resource path agree.
   The KBS response is encrypted to the attested ephemeral key.
5. The guest loads the OpenZFS key, imports the pool without mounting customer
   data, and verifies the encrypted generation marker against the remote
   manifest. Customer code starts only after the check succeeds.
6. After the runner exits, guestd prevents further runner access, writes the
   candidate marker, syncs and exports the pool, and obtains seal evidence
   bound to the marker digest. The worker snapshots only the exported outer
   zvol. The remote control plane promotes it only through parent CAS.
7. If the worker snapshots stale or torn ciphertext after a valid seal report,
   the next restore rejects the marker or OpenZFS authentication tree. The
   worker can cause a cache miss or outage, but not silent acceptance.

OpenZFS is selected because its authenticated encrypted blocks participate in
the filesystem's [end-to-end checksum
tree](https://openzfs.github.io/openzfs-docs/Basic%20Concepts/Checksums.html):
checksums live in parent block pointers, and the [native-encryption
format](https://openzfs.github.io/openzfs-docs/man/master/8/zfs-load-key.8.html#encryption)
uses 128 bits of checksum plus a 128-bit MAC for encrypted data. Replaying a
changed branch therefore requires replaying a coherent path to an older root,
which also replays the encrypted generation marker and fails the remote
freshness check. This construction still requires specialist review and
destructive validation; the selection is not evidence that OpenZFS has been
proved correct.

### Known costs and residual risks

- Nested host and guest ZFS increases guest memory use, write amplification,
  operational complexity, and cold-import latency. Production admission is
  blocked on representative CI benchmarks and crash/fault testing.
- OpenZFS native encryption has had historical encryption/send defects. This
  design never uses an inner logical `zfs send`; the worker snapshots and
  transfers the opaque outer zvol. The exact pinned version must still pass the
  upstream suite and Guardian's encrypted-pool battery before admission.
- OpenZFS native encryption does not hide pool topology, dataset and snapshot
  names, properties, file sizes, holes, or access timing. Names are fixed and
  non-customer-bearing; the remaining leakage is part of C10.
- A lineage wrapping-key compromise exposes every retained generation in that
  lineage. This is the cost of preserving block-level CoW clones. Tenant and
  trust-boundary separation are mandatory, and rekeying means a full copy into
  a new encryption root.
- The guest kernel and OpenZFS implementation join the TCB. dm-verity protects
  the immutable root from the worker, but a customer workload that compromises
  its own guest kernel can read and alter its own running workspace.
- The worker retains availability, timing, traffic-analysis, and side-channel
  power outside the explicit SNP claim.

## Alternatives considered and rejected

| Alternative | Decision and rationale | Reconsider only when |
| --- | --- | --- |
| Coconut SVSM | Rejected for the first production release. The project's [release process](https://coconut-svsm.github.io/svsm/RELEASE-PROCESS/) says development releases are not recommended for production, while its [attestation service](https://coconut-svsm.github.io/svsm/developer/ATTESTATION/) and [formal verification](https://coconut-svsm.github.io/svsm/developer/VERIFICATION/) are explicitly experimental. It would also add SVSM, IGVM, VMPL services, persistence, and host-proxy protocols to the TCB. | A final stable release is intended for production; the used attestation and persistence paths are no longer experimental; verification covers the exact security-critical path with exclusions reviewed; an independent audit is public or available to Guardian and material findings are closed; and the pinned integration passes the full Guardian battery. Re-audit then, do not adopt automatically. |
| LUKS2/dm-crypt plus dm-integrity and ext4 | Rejected for the persistent generation volume. It is a good standard construction for ephemeral scratch, but Confidential Containers [documents that its protected LUKS2 volume has no replay protection](https://confidentialcontainers.org/docs/features/protected-storage/confidential-emptydir/), and cryptsetup likewise [documents replay of older valid sectors](https://gitlab.com/cryptsetup/cryptsetup/-/blob/main/docs/v2.0.0-ReleaseNotes). A remote generation number catches whole-volume rollback but not selective valid-sector replay after verification. | A production-supported mutable authenticated block construction provides freshness across snapshots without a custom Merkle layer or full rewrite. LUKS may be reconsidered separately for non-persistent scratch where its rollback limitation is irrelevant; it is not part of this persistent-workspace design. |
| One data-encryption key per generation | Rejected because a CoW child still references its parent's ciphertext; OpenZFS explicitly [requires clones to share their origin's encryption key](https://openzfs.github.io/openzfs-docs/man/master/7/zfsprops.7.html#encryptionroot). Changing the data key requires re-encrypting every reachable block or introducing a new layered key system, destroying the millisecond clone property and adding custom cryptography. | Full-copy generations become acceptable, or a mature audited filesystem exposes independently rekeyable CoW descendants with the required integrity semantics. |
| Host-side OpenZFS native encryption | Rejected because the worker would load the key and see plaintext, directly violating the malicious-worker posture. | The worker is explicitly moved into the trusted boundary; that would be a different customer claim. |
| QEMU `microvm` machine type or Cloud Hypervisor | Rejected for the first confidential release. A smaller emulated-device surface is attractive, but the current warm-pool driver and golden-image contract already pin QEMU `pc-q35-8.2`. Stabilizing one launch-measurement and hardware-conformance tuple is lower risk than changing the VMM substrate during the SNP transition. | The first QEMU/q35 confidential tuple passes release gates, and a separately measured device-surface, compatibility, boot-latency, and escape-assessment comparison justifies migration. |
| dm-verity base plus writable overlay | Rejected for workspaces. dm-verity is excellent for the selected immutable guest root, but a mutable workspace needs an upper layer and eventual flatten/merge. Chains accumulate or every seal becomes a full copy. | The workspace becomes read-only, or measured full-copy cost is acceptable. |
| fscrypt, gocryptfs, or application-level encryption | Rejected because arbitrary CI expects a transparent POSIX filesystem, while these choices leave filesystem or operational metadata outside the same whole-workspace authenticated state and do not solve snapshot freshness. | The product constrains workloads to an application-aware storage API instead of arbitrary CI. |
| Custom verifier/KBS or Trustee LocalFs resources | Rejected. A bespoke verifier repeats high-risk evidence and certificate parsing; LocalFs leaves the only key repository on a KBS volume. Trustee plus the existing OpenBao custody and audit system is the smaller Guardian-specific surface. | Trustee cannot express a required fail-closed appraisal or resource policy after a documented upstream attempt; any replacement then requires independent protocol and implementation review. |
| External cloud KMS or OpenBao Transit wrapping | Rejected for the first release. Trustee must ultimately deliver the raw OpenZFS wrapping key to the attested guest, and v0.19.0 already has a built-in OpenBao-compatible KV backend. Another unwrap service adds credentials, availability, and custom integration without removing Trustee from the trusted path. | A customer-managed/HSM requirement appears, or Trustee gains a supported envelope backend that measurably reduces compromise scope without exposing keys to the worker. |
| VLEK or masked chip identity | Rejected for Guardian-owned bare metal. VCEK gives direct chip and TCB binding, and an unmasked chip ID lets the remote verifier obtain and validate the correct endorsement. Chip identity remains pseudonymous and access-controlled. | A managed platform requires VLEK and provides a reviewed provisioning, revocation, and ownership model. |
| SMT enabled | Rejected for the first release to reduce shared-core side-channel exposure and make the admitted topology unambiguous. | A workload-specific side-channel assessment and benchmark justify enabling it, followed by a new policy and evidence tuple. |
| Live migration, SNP migration agents, RAM snapshots, or customer-bearing vmstate | Rejected because they add key-sharing, state-freshness, and memory-lifecycle protocols before the cold-boot design is proven. | A separately reviewed migration protocol preserves measurement, tenant binding, rollback protection, and key erasure and passes a new hostile-worker campaign. |

## Threat model

### Adversary A: compromised worker

For customer confidentiality and integrity, assume the worker is Byzantine.

The adversary controls:

- root on the worker host, its kernel, KVM-facing userspace, QEMU, hostd, QMP,
  cgroups, namespaces, firewall, and local observability;
- ZFS datasets, snapshots, block-device contents and metadata, ordering of
  reads and writes, TRIM behavior, and all local backups;
- virtual devices, MMIO, shared pages, interrupts, CPUID responses, wall
  clock, virtio RNG input, serial console, vsock transport, and guest network;
- VM launch arguments, firmware and disk substitution, reboots, crashes,
  scheduling, packet delay, duplication, replay, reordering, and loss;
- physical console and BMC-equivalent administration of the worker, short of
  sophisticated invasive attacks against the CPU package or AMD manufacturing
  root of trust.

The worker must not run Trustee, OpenBao, the authoritative generation
manifest, the release-policy store, or the only copy of their audit logs.

### Adversary B: malicious customer workload

Assume every CI job can run a hostile kernel and has an undisclosed guest-to-host
or guest-kernel exploit. It attempts to:

- escape QEMU/KVM and compromise the worker;
- read or modify another tenant's disks, memory, network, or metadata;
- reach the management cluster, Trustee/OpenBao administration, hostd
  control surfaces, or node credentials;
- exhaust CPU, memory, PIDs, I/O, network, ZFS, attestation, and key-release
  capacity;
- leave state for the next job or corrupt generation bookkeeping.

SEV-SNP protects the customer from the worker. KVM, the VMM jail, network
policy, device minimization, and lifecycle destruction protect Guardian and
other customers from the customer. Both directions are required.

### Trusted components

The initial trusted computing base is:

- the AMD CPU and AMD endorsement root, subject to an explicit minimum TCB and
  active security-bulletin policy;
- stateless OVMF, the guest kernel and initramfs, the dm-verity root and root
  hash, guestd, OpenZFS, cryptographic libraries, and security configuration;
- Trustee KBS, Attestation Service, RVPS, OpenBao, and their policy, transport,
  authorization, and signing keys;
- the authoritative generation manifest and compare-and-swap control plane;
- the source-to-artifact build, signing, provenance, and reference-measurement
  publication path.

### Explicit non-claims

The confidential-computing claim does not promise:

- availability in the face of a malicious worker;
- invisibility of ciphertext sizes, I/O volume, job timing, CPU usage, network
  endpoints, or other explicitly documented metadata;
- protection from arbitrary side channels or undisclosed CPU vulnerabilities;
- safety of customer code that willingly exfiltrates its own secrets;
- protection after compromise of Trustee, OpenBao, the control plane, guest
  image signing identity, or AMD root of trust;
- protection against long-term invasive CPU-package attacks or malicious CPU
  manufacture.

Availability remains a Postflight reliability obligation: worker-induced
failure must be bounded, observable, fail closed, and recoverable elsewhere.

## Security claims

| Claim | Required property |
| --- | --- |
| C1 — memory confidentiality | Worker administration and VMM interfaces cannot recover customer plaintext or guest keys from private guest memory. |
| C2 — attested key release | A workspace key is released only to a fresh, approved launch and only for its authorized tenant, scope, and generation. |
| C3 — storage confidentiality | Every customer-bearing workspace block stored by the worker, ZFS, or backup system is guest-encrypted ciphertext. |
| C4 — storage integrity and freshness | Block corruption, cross-tenant substitution, generation substitution, rollback, and lineage forks are detected before customer code consumes the generation. |
| C5 — tenant isolation | A hostile job cannot access another tenant or compromise worker/control-plane authority. |
| C6 — lifecycle non-persistence | A VM is never reused across jobs; keys, JIT credentials, and plaintext do not survive destruction in an accessible form. |
| C7 — software identity | Every accepted launch measurement maps to reviewed, signed, and reproducibly built guest artifacts and an explicit TCB manifest. |
| C8 — fail-closed behavior | Ambiguous attestation, mount, quiesce, seal, policy, or bookkeeping states never release a key or promote a generation. |
| C9 — auditable decisions | Every policy/reference-value change and key-release decision is attributable and retained in a tamper-evident system outside the worker. |
| C10 — bounded disclosure | Metadata visible to the worker is measured, documented, and does not accidentally include customer plaintext or credentials. |

## Design preconditions

Testing cannot rescue a protocol that lacks these properties. They are entry
criteria for the hardware security-validation lane:

1. **Two attestation contexts.** Boot/unlock attestation authorizes reading an
   existing generation. Seal attestation authorizes recording the resulting
   generation-marker digest and promoting a candidate. A seal-time report
   cannot retroactively secure initial key release.
2. **Fresh, key-bound evidence.** `REPORT_DATA` commits to Trustee's runtime
   data, including its fresh nonce and the guest-generated ephemeral JWK.
   `HOST_DATA` commits to signed init data containing the fixed protocol
   version, lease `jti` and expiry, tenant, scope, lineage, generation, expected
   parent, marker digest, and requested resource identifier. Trustee verifies
   both bindings in one report.
3. **Attestation is not scheduling authorization.** A correctly measured guest
   receives no key or JIT configuration without a fresh control-plane lease
   authorization bound to the same ephemeral public key and exact generation.
4. **Remote authorization and custody.** Trustee, OpenBao, release policy, key
   authority, and the authoritative generation manifest are not on the worker.
5. **Lineage-scoped encryption roots.** Every tenant/workspace lineage has a
   random 256-bit OpenZFS wrapping key. CoW generations within that lineage
   reuse it; crossing a trust boundary or rotating after compromise creates a
   new encryption root through a full copy.
6. **Authenticated storage and external freshness.** Guest-side OpenZFS native
   encryption authenticates the CoW block-pointer tree. A deterministic-CBOR
   generation marker inside that tree is checked against the remote manifest,
   which owns lineage, accepted generation, marker digest, and parent CAS.
7. **Verify before execution.** The restored generation marker and encrypted
   filesystem state are verified before any customer-controlled code runs.
8. **No customer-bearing vmstate.** RAM/vmstate snapshots and cross-job VM
   resurrection are prohibited for the first confidential release.
9. **No host plaintext path.** The worker never receives a plaintext workspace
   key and never mounts the customer filesystem.
10. **Policy denies by default.** Unknown hardware, report versions, processor
   generations, measurements, policies, TCB values, tenants, resources, or
   protocol versions are rejected.
11. **Signed reference values.** The source-to-artifact pipeline produces an
    authenticated mapping from release artifacts to expected launch
    measurement and policy. QEMU arguments alone are not the SNP measurement.

## Implementation sequence

No phase may admit external customer secrets. A phase can advance only after
its exit gate passes on the exact artifact and hardware tuple used by the next
phase.

| Phase | Deliverable | Exit gate |
| --- | --- | --- |
| 1 — hardware/VMM tuple | Declare the rs4 processor family, BIOS, microcode, SNP firmware, VCEK cache, SMT-off topology, host kernel, QEMU, stateless OVMF, q35 ABI, and minimal devices. | Reproducible launch measurements, VCEK verification, TCB rejection, and hardware inventory tests pass without releasing a workspace key. |
| 2 — measured guest | Direct measured kernel/initramfs boot, dm-verity read-only root, signed build/reference publication, ephemeral session key, and direct VMPL0 SNP reporting. | OVMF, kernel, initramfs, command-line, root-hash, and rootfs mutation tests all fail closed; no writable NVRAM or customer-bearing vmstate exists. |
| 3 — remote authorization | Deploy Trustee v0.19.0 KBS/AS/RVPS and its restrictive policies in the management cluster; configure the verified-TLS, read-only OpenBao KV backend; bind attestation, active lease, resource, and encrypted JIT assignment to one guest ephemeral key. | The full attestation and key-release negative matrix passes, including an approved but unscheduled guest receiving zero secret bytes. |
| 4 — encrypted generations | Create guest OpenZFS pools with the selected properties; implement lineage LWK lifecycle, pre-mount marker verification, quiesce/export/seal, outer-zvol snapshot, remote manifest, and parent CAS. | Storage corruption, selective replay, whole rollback, fork, crash-point, backup/restore, residue, and cryptographic-erasure tests pass. Existing plaintext generations are not migrated or grandfathered; confidential lineages start empty and checkout repopulates them. |
| 5 — adversarial qualification | Integrate the VMM jail, network isolation, fault harness, nightly hostile-worker battery, continuous canary, benchmarks, and evidence bundle. Commission independent architecture, protocol/storage, malicious-worker, and guest-escape assessments. | Every release blocker passes, benchmark limits are accepted, no critical/high finding remains, and customer language matches the measured claims. |
| 6 — production admission | Enable only evidence-current tuples for synthetic traffic, then explicitly admitted customer scopes. | Canary and operational drills remain healthy. A failing or expired tuple is drained and denied key release; there is no plaintext fallback. |

Rollback before customer admission means disabling confidential scheduling and
discarding synthetic lineages. After admission, rollback means denying the
affected tuple and restoring an encrypted generation on another passing tuple;
it never means trusting the worker, exporting plaintext, or bypassing Trustee.

## Execution lanes

| Lane | Trigger | Environment | Gate |
| --- | --- | --- | --- |
| PR | Every relevant change | Hermetic Bazel tests; captured reports and simulated devices | Parser, policy, protocol, state-machine, and mutation tests must pass. |
| Fuzz | Continuous and before release | Sanitized userspace targets and upstream fuzz harnesses | No known crash; corpus and coverage retained. |
| Hardware conformance | Every image, kernel, QEMU, firmware, policy, or hardware tuple | Dedicated sacrificial SNP worker with production-equivalent configuration | All release-blocking hardware tests pass. |
| Hostile-worker battery | Nightly and before promotion | Dedicated sacrificial worker controlled by the fault harness | All confidentiality, freshness, and fail-closed assertions pass. |
| Continuous canary | Production, with synthetic non-customer secrets | Normal scheduler and one designated confidential canary scope | Attestation, key release, encrypted restore, run, seal, destroy, and residue scan remain healthy. |
| Operational drill | Quarterly and after material recovery changes | Dedicated sacrificial infrastructure, one node/failure domain at a time | Revocation, outage, rebuild, recovery, and evidence procedures meet their security and recovery contracts. |
| External assessment | Before first customer, annually, and after material protocol/TCB changes | Source access plus dedicated hardware | Critical/high findings fixed and retested; accepted residual risks published internally. |

Destructive worker tests run on one explicitly designated sacrificial node at a
time. Production configuration still converges through Flux; the test harness
must not become a second administrative control plane.

## Test catalog

Legend:

- **PR** — hermetic change gate.
- **F** — continuous or release fuzzing.
- **R** — release blocker for every admitted hardware/software tuple.
- **N** — hostile-worker nightly battery.
- **C** — continuous synthetic canary.
- **Q** — quarterly operational/incident drill.
- **E** — external assessor or specialist exercise.

### Foundations and assurance inventory

| ID | Lane | Test | Pass condition |
| --- | --- | --- | --- |
| BAS-001 | R | Machine-readable TCB inventory | OVMF, kernel, initramfs, dm-verity root, guestd, OpenZFS, QEMU, host kernel, Trustee, OpenBao, policies, reference set, and crypto libraries have immutable digests and owners. |
| BAS-002 | R | Claims-to-tests traceability | Every claim C1–C10 maps to at least one release test, one production signal where possible, and an explicit residual risk. |
| BAS-003 | R | Source-to-measurement chain | A signed release manifest maps reviewed source and build provenance to exact guest artifacts, launch measurement, host data, and accepted policy. |
| BAS-004 | R | Exact configuration inventory | The actual QEMU argv, device list, SNP policy, kernel command line, dm-verity root, OpenZFS properties, and network policy match the reviewed manifest. |
| BAS-005 | R | Toolchain pinning | Every security-test tool and corpus is version/digest pinned; test output records the versions used. |
| BAS-006 | R | No sample/insecure mode | Sample evidence, `AllowAll`, insecure HTTP/TLS verification, insecure token keys, debug builds, test KBS, LocalFs resources, and empty reference sets are impossible in the production render. |
| BAS-007 | R | Residual-risk review | Availability, metadata, side-channel, physical, AMD-root, and management-plane assumptions are current and customer claims do not exceed them. |
| BAS-008 | R,C | Admission guard | A node or image tuple lacking a current passing evidence bundle receives no customer-bearing confidential jobs. |

### Hardware and firmware posture

| ID | Lane | Test | Pass condition |
| --- | --- | --- | --- |
| HW-001 | R,C | SNP capability probe | The node exposes the expected SNP/KVM/guest-report interfaces and the pinned `snphost ok` probe succeeds. |
| HW-002 | R | BIOS and platform configuration | SNP, IOMMU, required memory protections, Secure Boot policy, and prohibited debug/migration features match the approved node profile. |
| HW-003 | R,C | Minimum TCB | Reported bootloader, TEE, SNP firmware, microcode, and FMC where applicable meet current minimums derived from AMD bulletins. |
| HW-004 | R | Below-minimum TCB rejection | A validly signed report advertising a prohibited TCB is denied resource release. |
| HW-005 | R | Reported/platform/committed TCB transitions | Firmware update and commit states produce the expected reports; no old state remains unintentionally accepted. |
| HW-006 | R | Processor-generation dispatch | Unknown family/model/report-version combinations are rejected rather than parsed using a nearby generation's rules. |
| HW-007 | R | Unmasked chip identity | Reports contain the unmasked chip ID required for VCEK verification; masking or a VCEK for another chip is denied. |
| HW-008 | R | SMT and socket policy | SMT is disabled in platform configuration and prohibited by guest policy; the admitted guest topology is one socket. |
| HW-009 | R,N | Endorsement acquisition outage | AMD KDS loss uses only a valid, bounded-age offline cache or fails closed; it never trusts host-supplied unverified certificates. |
| HW-010 | R | Endorsement and revocation refresh | ARK/ASK/ASVK/VCEK material and revocation data refresh on schedule, with stale/poisoned cache tests; VLEK evidence is denied. |
| HW-011 | R | Bulletin emergency gate | Injecting a newly prohibited TCB or platform condition removes the node from key-release eligibility without rebuilding guests. |
| HW-012 | E | Basic physical-host review | BMC, console, DMA-capable ports, crash dump, cold-boot, and service procedures do not introduce an undocumented plaintext path. |

### Attestation evidence and appraisal

| ID | Lane | Test | Pass condition |
| --- | --- | --- | --- |
| ATT-001 | R,C | Valid boot evidence | The exact approved launch obtains an affirming result and no broader launch does. |
| ATT-002 | R | Report signature mutation | Mutating any signed report byte causes verification failure. |
| ATT-003 | R | Certificate-chain substitution | Self-signed, wrong-root, wrong-generation, reordered, truncated, expired, or malformed chains fail. |
| ATT-004 | R | VEK/report TCB mismatch | Certificate SPL extensions that differ from `REPORTED_TCB` fail. |
| ATT-005 | R | Chip ID mismatch | A VCEK from a different processor cannot validate the report. |
| ATT-006 | R | Report-version bounds | Reports below the minimum or above explicitly supported ABI versions fail closed. |
| ATT-007 | R | VMPL | Only evidence requested by the direct VMPL0 guest can authorize key release; every other VMPL value is denied. |
| ATT-008 | R | Firmware mutation | A one-bit change to stateless OVMF produces an unrecognized measurement and is denied. Writable or substituted NVRAM is rejected. |
| ATT-009 | R | Kernel mutation | A one-bit kernel change is denied. |
| ATT-010 | R | Initramfs mutation | A one-bit initramfs change is denied. |
| ATT-011 | R | Command-line mutation | Removing an integrity, console, module, lockdown, or workspace-security parameter is denied. |
| ATT-012 | R | Verified guest root | A changed dm-verity root hash is denied by measurement; changing guestd, OpenZFS, the attestation client, or rootfs content under the admitted hash fails verification before execution. |
| ATT-013 | R | Host-data binding | Wrong or absent `HOST_DATA`, launch-authorization digest, signature, audience, lease `jti`, expiry, or initial-data content is denied. |
| ATT-014 | R | Debug policy | A correctly signed report with debug allowed is denied. |
| ATT-015 | R | Migration policy | A correctly signed report permitting any migration agent is denied. |
| ATT-016 | R | SMT/platform policy | A signed report with SMT allowed or another prohibited platform flag is denied. |
| ATT-017 | R | Missing reference values | Missing measurement or hardware reference values yield a contraindicated/denied result, never "unknown but allowed." |
| ATT-018 | R | Reference-value mismatch | Each incorrect hardware, measurement, ABI, policy, or platform reference independently denies. |
| ATT-019 | R | Nonce replay | Reusing a previously accepted report against a new challenge fails. |
| ATT-020 | R | Nonce mismatch | A report bound to another verifier challenge fails. |
| ATT-021 | R | Concurrent challenges | Reports for simultaneous sessions cannot be swapped, raced, or consumed twice. |
| ATT-022 | R | Ephemeral-key substitution | Replacing the guest public key after report generation prevents channel establishment and secret recovery. |
| ATT-023 | R | Context substitution | Altering the Trustee nonce or ephemeral JWK breaks `REPORT_DATA`; altering tenant, scope, lineage, generation, parent, marker digest, resource ID, lease authorization, or protocol version breaks the signed-init-data/`HOST_DATA` binding. |
| ATT-024 | R | Canonical transcript | Alternate JWS encodings, algorithm/header changes, duplicate claims, Unicode forms, truncation, concatenation ambiguity, and cross-protocol inputs cannot produce an accepted binding. |
| ATT-025 | R | Evidence composition | If additional evidence is used, removal, replacement, or recombination with another primary report fails. |
| ATT-026 | PR,F | Parser mutation corpus | Truncated, oversized, duplicate-field, invalid-enum, invalid-DER, integer-boundary, and random reports never panic, hang, or bypass policy. |
| ATT-027 | N | Worker MITM | Worker interception, replay, duplication, delay, and reordering of the attestation exchange cannot produce an accepted stale or misbound session. |
| ATT-028 | R | Verifier authentication | The guest/key client authenticates the intended Trustee deployment and tenant before disclosing evidence or accepting encrypted resources. |
| ATT-029 | R | Appraisal versus authorization separation | Affirming hardware evidence alone cannot fetch a resource; Trustee policy also requires an active control-plane lease bound to the attested key and exact resource context. |
| ATT-030 | R | Decision provenance | Each result records evidence digest, policy digest, reference-set digest, verifier build, decision, and pseudonymous platform identity outside the worker. |

### Trustee and policy governance

| ID | Lane | Test | Pass condition |
| --- | --- | --- | --- |
| VER-001 | R | Policy default deny | Empty, invalid, partially loaded, timed-out, or unknown-version policies deny. |
| VER-002 | R | Policy tamper | Unauthorized policy or reference-value changes are rejected and alerted. |
| VER-003 | R | Policy rollback | Restoring an older validly signed policy/reference set without an authorized rollback record is rejected. |
| VER-004 | R | Policy rollout | Dual-allow measurement rollouts accept only the explicitly bounded old/new set and remove the old set after convergence. |
| VER-005 | R | Decision reproducibility | Archived evidence plus archived policy/reference digests reproduces the original decision. |
| VER-006 | R | Administrative authentication | Policy, reference, resource, and signing-key APIs require separate least-privilege identities; unauthenticated and worker identities fail. |
| VER-007 | R | Tenant isolation | One Trustee tenant cannot read/change another tenant's policies, references, evidence, results, keys, or quotas. |
| VER-008 | N | Noisy-neighbor resistance | Malformed or high-rate evidence from one tenant cannot cause another tenant to bypass policy or exceed the agreed release latency/error budget. |
| VER-009 | R | Signing/encryption key rotation | Old verifier keys expire as planned; compromise rotation preserves verifiability of history without accepting new signatures from revoked keys. |
| VER-010 | R | Tamper-evident history | Policy, key, reference, administration, and decision histories survive worker compromise and produce detectable evidence on deletion/reordering. |
| VER-011 | N | Trustee outage | Outage yields bounded job failure or retry without fail-open key release. |
| VER-012 | E | Trustee implementation review | Binary parsing, certificate validation, policy inputs, cache behavior, crypto use, and error handling receive specialist source review. |

### Attestation-bound key release

The released secret is the OpenZFS lineage wrapping key (LWK). Generation
authorization is exact even though related CoW generations intentionally share
that LWK.

| ID | Lane | Test | Pass condition |
| --- | --- | --- | --- |
| KEY-001 | R,C | Authorized release | Only an approved fresh session with a matching active lease obtains the requested generation's authorized LWK encrypted to its attested ephemeral key. |
| KEY-002 | R | Denied release has zero secret bytes | Every failed evidence or policy case returns no plaintext, wrapped key, distinguishable partial secret, or reusable release handle. |
| KEY-003 | R | Tenant isolation | An approved guest for tenant A cannot request tenant B's key. |
| KEY-004 | R | Scope isolation | A guest for one repo/workflow/job scope cannot request another scope's key. |
| KEY-005 | R | Generation authorization | A guest authorized for generation N cannot use that authorization to unlock N-1, N+1, or a sibling, even when those generations share its lineage LWK. |
| KEY-006 | R | Parent/marker binding | A request with an unexpected parent or marker digest is denied before key release. |
| KEY-007 | R | Resource-identifier confusion | Path traversal, alternate encodings, case changes, aliases, duplicate separators, and prefix collisions cannot select another key. |
| KEY-008 | R | Token/session replay | A lease `jti` is consumed once by the control plane; an attestation token, JIT assignment, encrypted response, nonce, or release handle cannot be reused with another TEE public key, resource context, session, or expired lease. |
| KEY-009 | R | Response key substitution | A worker cannot redirect the encrypted LWK to a key it controls or to another guest. |
| KEY-010 | N | Transport MITM | TLS/vsock/network interception cannot read the LWK or JIT assignment, alter authorization context, or downgrade channel security. |
| KEY-011 | R | Key hierarchy | Every tenant/workspace lineage has one independently generated 256-bit LWK; generations in that lineage reuse it, while cross-tenant, cross-lineage, and new-encryption-root keys never collide. |
| KEY-012 | R | Key lifecycle trace | Creation, wrap, release, rotation, revocation, recovery, and destruction events are attributable without logging key material. |
| KEY-013 | R | Worker exclusion and least privilege | Root on a worker cannot authenticate to Trustee/OpenBao administration or request raw LWKs. Trustee's OpenBao token is management-cluster-only, TLS-verified, read-only, and confined to the Postflight LWK mount/path. |
| KEY-014 | R | Logging and telemetry | KBS/verifier errors, traces, metrics, profiles, and audit records contain no plaintext key or decryptable release payload. |
| KEY-015 | N | Guest crash after release | Crashing at each point after release leaves no host-readable key and does not authorize promotion. |
| KEY-016 | R | Guest zeroization | Key buffers are bounded in lifetime, excluded from core dumps, and made unreachable on unmount/destroy; a residue scan finds no synthetic key marker. |
| KEY-017 | R | Revocation | Revoking a tenant, generation authorization, measurement, lineage, or LWK prevents all affected new releases within the documented bound. |
| KEY-018 | R | Trustee/OpenBao recovery | Restoring Trustee policy/reference state and OpenBao KV data preserves authorized recovery without broadening policy, changing LWK bytes, or requiring the failed worker. |
| KEY-019 | R | Cryptographic erasure | After every generation in a lineage is reaped, deleting its final OpenBao LWK resource and authorized backup copies makes the lineage ciphertext unrecoverable under the documented destruction model. |
| KEY-020 | E | Protocol review | A cryptography/protocol specialist reviews transcript construction, freshness, channel binding, algorithms, key hierarchy, and compromise scope. |

### Workspace encryption, integrity, and generation freshness

| ID | Lane | Test | Pass condition |
| --- | --- | --- | --- |
| STO-001 | R,C | Plaintext marker scan | High-entropy and structured canary secrets written in the guest are absent from raw zvols, ZFS snapshots, host page cache, logs, swap, and backups. |
| STO-002 | R | Host cannot import plaintext | The worker can snapshot and copy only the outer raw zvol; without an attested LWK it cannot load the inner encryption root, mount the workspace, or recover file and directory plaintext. |
| STO-003 | R | Wrong lineage key | A different tenant or lineage LWK cannot load the encryption root. A different generation in the same lineage can be decrypted only after independent release and is still rejected when its marker is not the authorized generation. |
| STO-004 | R | Cross-tenant snapshot substitution | Attaching tenant A's encrypted pool to tenant B's authorized guest fails by key and marker checks before customer code runs. |
| STO-005 | R | Cross-scope substitution | Attaching another repo/job scope's generation, including one sharing a lineage LWK, fails the encrypted marker check. |
| STO-006 | R | Authenticated-block corruption | Single-bit ciphertext, tag, block-pointer, or encrypted-metadata modification is detected by OpenZFS and cannot yield silently modified plaintext. |
| STO-007 | R,N | Block/subtree replay and reordering | Replaying, duplicating, swapping, or reordering selected valid blocks or an old subtree fails an authenticated parent path, or coherently rolls back the marker and is rejected. |
| STO-008 | R,N | Whole-generation rollback | Replacing generation N with a coherent older pool/uberblock for N-1 is rejected by the authoritative marker digest. Import rewind/recovery flags cannot bypass the expected generation. |
| STO-009 | R,N | Fork detection | Concurrent children of the same parent produce at most one current successor; losing branches cannot later masquerade as current. |
| STO-010 | R | Parent substitution | A valid generation presented with a false parent/lineage is rejected. |
| STO-011 | R | Snapshot identity tamper | Changing outer dataset names, snapshot GUIDs, inner pool metadata, host journal, or local generation records cannot override the remote manifest and encrypted marker. |
| STO-012 | R | Device truncation/extension | Shrinking, extending, sparse-hole substitution, label replacement, or geometry changes fail safely without an import mode that discards verification. |
| STO-013 | R | Expected marker before run | The guest imports without exposing the mount to the runner, verifies lineage, scope, generation, parent, and marker digest, and only then publishes the workspace-ready marker or starts checkout. |
| STO-014 | R | Logical-state mutation | A one-byte logical filesystem or generation-marker change either fails OpenZFS authentication or produces a different marker digest and cannot be accepted under the old remote manifest. |
| STO-015 | R | Quiesce failure | Runner-stop, sync, pool export, I/O, guest deadline, or malformed seal-evidence failure skips the outer snapshot and promotion. |
| STO-016 | R | Failed/cancelled job | Failure, cancellation, stale attempt, unknown conclusion, or PR-only execution never advances the trusted generation. |
| STO-017 | N | Torn write and power loss | Kill/reboot at each write, marker, sync, pool-export, outer-snapshot, seal-report, CAS, and cleanup boundary yields either the prior accepted generation or one completely verified successor. |
| STO-018 | N | Host journal tamper | Deleting/reordering local journal state cannot cause a candidate to be remotely accepted or reaped while referenced. |
| STO-019 | R | Backup/restore | Outer-zvol snapshots and send streams remain ciphertext; restore requires fresh attestation and exact generation authorization and preserves marker-based rollback protection. |
| STO-020 | R | Nested-ZFS clone and transfer | Outer ZFS clone, snapshot, send/receive, sparse allocation, and backup paths create no plaintext or cross-tenant reference; inner encryption, checksum, compression-off, dedup-off, and autotrim-off properties cannot drift. |
| STO-021 | R | Deleted generation | Reaped generations are no longer releasable, and stale workers cannot resurrect their key authorization. |
| STO-022 | E | Storage construction review | OpenZFS native-encryption internals, block-pointer authentication, pool import/rewind behavior, lineage-key scope, marker protocol, nested snapshot semantics, and crash consistency receive specialist review. |

### Hostile-worker runtime and virtual-device tests

The Linux [confidential-guest threat
model](https://docs.kernel.org/security/snp-tdx-threat-model.html) is the
baseline: the host controls devices, shared memory, interrupts, and
availability even when private memory and registers are protected.

| ID | Lane | Test | Pass condition |
| --- | --- | --- | --- |
| HST-001 | R,E | Host memory extraction | `/proc`, debugger, QMP dump, crash dump, KVM interfaces, and privileged host tooling cannot recover synthetic private-memory or key markers. |
| HST-002 | R | Private/shared page inventory | Security-critical buffers never become shared; only documented transport/DMA buffers do. |
| HST-003 | N | Shared-page content scan | The worker continuously scans shared pages during a canary and observes no plaintext key, JIT credential, or workspace content beyond the explicit protocol. |
| HST-004 | R,N | Malicious virtio RNG | Zero, repeated, biased, delayed, and absent virtio entropy cannot cause repeated/predictable guest ephemeral keys or nonce reuse. |
| HST-005 | N | Clock manipulation | Backward/forward jumps, frozen time, bad RTC, and inconsistent clocks cannot extend authorization or bypass freshness. |
| HST-006 | N | DNS/TLS MITM | Host-controlled DNS, routes, certificates, and packet contents cannot redirect the guest to unauthenticated Trustee/control-plane endpoints or expose secrets. |
| HST-007 | N | Vsock manipulation | Injection, truncation, replay, reordering, duplication, cross-VM delivery, and CID reuse cannot alter authenticated protocol meaning. |
| HST-008 | R,N | Unexpected device/hotplug | Unmeasured or unauthorized device addition, removal, or hotplug is rejected or harmless and cannot introduce a secret path. |
| HST-009 | F,N | Virtio block/SCSI fuzz | Malformed device responses, lengths, status codes, completion order, and resets do not compromise the guest TCB or silently corrupt accepted storage. |
| HST-010 | F,N | Virtio net/vsock fuzz | Malformed packets/descriptors and reset races do not compromise guest key or attestation processes. |
| HST-011 | F,E | GHCB/#VC protocol | Invalid exception injection, GHCB messages, CPUID responses, and shared/private transitions do not produce unsafe guest behavior. |
| HST-012 | N | Interrupt/reset storm | Interrupt storms, device resets, vCPU pauses, and forced reboots fail the job without release/promotion confusion. |
| HST-013 | N | Kill-point matrix | QEMU/hostd/worker is killed before, during, and after attestation, lease authorization, key release, pool import, marker check, run, export, seal, CAS, and destroy; every state recovers deterministically. |
| HST-014 | R | RAM snapshot prohibition | QMP migration/savevm/dump and supervisor policy cannot create or restore customer-bearing vmstate. |
| HST-015 | R | Migration/swap policy | SNP migration-agent and prohibited ciphertext move/swap controls remain disabled and are checked in evidence. |
| HST-016 | R,N | Serial/console disclosure | OVMF, kernel, initramfs, guestd, OpenZFS, and panic paths emit no secret or workspace plaintext to host-visible console channels. |
| HST-017 | N | Disk availability deception | Delayed, dropped, duplicated, and transiently failing I/O never causes a corrupt generation to be accepted. |
| HST-018 | E | Host-side side-channel exercise | Measure cache, page-fault, scheduling, ciphertext movement, timing, I/O, and traffic leakage against documented expectations; findings adjust claims or mitigations. |

### Host and tenant isolation against a malicious guest

| ID | Lane | Test | Pass condition |
| --- | --- | --- | --- |
| ISO-001 | R,C | Minimal VMM surface | Exact argv contains only approved virtio devices plus `-nodefaults`, `-no-user-config`, no graphical/legacy devices, and the reviewed SNP object/policy. |
| ISO-002 | R | Non-root VMM | Every QEMU process has a unique unprivileged identity and no ambient privilege or worker-admin group access. |
| ISO-003 | R | Seccomp/no-new-privs | Forbidden process, privilege, mount, namespace, ptrace, raw-device, and resource-control syscalls fail. |
| ISO-004 | R | Filesystem jail | QEMU sees only its immutable runtime and assigned disks/sockets; host root, other tenants, credentials, and control-plane material are unreachable. |
| ISO-005 | R | MAC isolation | AppArmor/SELinux labeling prevents an escaped or confused QEMU process from opening another VM's resources. |
| ISO-006 | R,N | Cgroup containment | CPU, memory, PIDs, I/O, and network exhaustion remain inside the per-VM limits and do not disable Trustee/OpenBao/control-plane service. |
| ISO-007 | R | QMP and local sockets | QMP, serial, pidfiles, state directories, and vsock endpoints have per-VM ownership and cannot be reached by the guest or another QEMU UID. |
| ISO-008 | R | Disk ownership | A guest can address only its root and workspace devices; serial spoofing, hotplug requests, path traversal, and stale handles cannot select another zvol. |
| ISO-009 | R,N | Network cross-tenant denial | ARP/NDP spoofing, MAC/IP spoofing, VLAN hopping, multicast, broadcast, and direct routing cannot reach another VM. |
| ISO-010 | R,N | Management-plane denial | Workload networks cannot reach Kubernetes/Talos/OpenBao administration, Trustee admin APIs, hostd control, node-local credentials, BMC, or storage administration. |
| ISO-011 | R | Metadata endpoint | Any guest metadata endpoint is authenticated, minimal, tenant-scoped, replay-safe, and contains no worker credential. |
| ISO-012 | F,E | QEMU device fuzz | Run upstream QEMU qtest/generic fuzzers for every exposed device, including virtio-scsi/block/net/rng/vsock equivalents where available. |
| ISO-013 | F,E | KVM attack surface | Run kernel KVM selftests, kvm-unit-tests, syzkaller/KVM campaigns, and targeted public guest-to-host exploit regressions against the pinned host kernel. |
| ISO-014 | E | VMM escape assessment | External testers receive a hostile kernel and attempt QEMU, KVM, QMP, supervisor, namespace, MAC, and device escape. |
| ISO-015 | N | Hostd authentication/confusion | A guest cannot impersonate another CID/boot ID/lease, send lifecycle messages for another VM, or make hostd attach/seal the wrong disk. |
| ISO-016 | R,C | Workload-node credential absence | Host and guest residue scans find no standing management-cluster admin, signing, OpenBao root, GitHub App private, Trustee admin, or OpenBao KBS credential. |

### Job and VM lifecycle

| ID | Lane | Test | Pass condition |
| --- | --- | --- | --- |
| LIF-001 | R,C | One job per VM | Every job observes a new VM identity/boot ID and no VM accepts a second assignment. |
| LIF-002 | R | Customer-free golden image | Full image and runtime-root scans contain no prior customer workspace, key, JIT token, repository object, machine identity, or host key. |
| LIF-003 | R | No customer vmstate | No customer-bearing memory snapshot is created, promoted, backed up, or restored. |
| LIF-004 | R,N | JIT credential residency | The JIT configuration remains in private guest RAM only and is absent from disks, argv, environment exposed to host, logs, and crash artifacts. |
| LIF-005 | R,N | Cleanup matrix | Success, failure, cancellation, timeout, guest panic, QEMU crash, hostd crash, and worker reboot all destroy or quarantine VM/zvol state and refill safely. |
| LIF-006 | PR,N | Crash-point state model | Every durable transition has an injected crash test and a model-checked recovery classification; vacuity mutants prove assertions can fail. |
| LIF-007 | R,N | Promotion CAS | Racing successful jobs yield exactly one current generation based on the observed parent. |
| LIF-008 | R | PR write isolation | Pull-request/fork-like read scopes never promote writes to a trusted default-branch lineage. |
| LIF-009 | N | Stale worker messages | Delayed reports from destroyed/replaced VMs cannot mutate the current lease or generation. |
| LIF-010 | C | Residue canary | After every canary, no VM, temporary zvol, imported guest pool, loaded ZFS key, key handle, socket, or nonterminal operation remains beyond its bound. |

### Supply chain and reference-value publication

| ID | Lane | Test | Pass condition |
| --- | --- | --- | --- |
| SUP-001 | R | Artifact signatures and provenance | OVMF, kernel, initramfs, dm-verity root, guestd, OpenZFS, QEMU, Trustee, and OpenBao artifacts verify against exact authorized build identities and SLSA provenance. |
| SUP-002 | R | Reproducibility | Independent rebuilds reproduce security-critical artifacts or documented non-reproducible fields are removed before measurement publication. |
| SUP-003 | R | Measurement reproduction | The release job and an independent verifier calculate the same SNP launch measurement from the published artifacts/configuration. |
| SUP-004 | R | Tampered artifact | Changing any artifact after signing causes signature, provenance, digest, or measurement admission failure. |
| SUP-005 | R | Reference publication authorization | Only the release/reference publishing identity can add measurements; pull requests, workers, Kargo image promotion, and registry state cannot self-authorize. |
| SUP-006 | R | Registry distrust | Registry substitution, tag movement, missing artifact, and alternate manifest attacks fail because all admitted inputs are digest-pinned and signature checked. |
| SUP-007 | R | Dependency and unsafe-code review | Known-vulnerability scan, license inventory, unsafe-code inventory, and cryptographic dependency review cover every TCB release. |
| SUP-008 | R | Security update SLA | QEMU, KVM/kernel, OVMF, dm-verity, OpenZFS, crypto, Trustee, and OpenBao advisories have owners, severity policy, and emergency measurement/TCB revocation procedure. |
| SUP-009 | R | Build/reference separation | Compromise of artifact storage alone cannot publish a trusted measurement; compromise of a worker cannot sign builds or references. |
| SUP-010 | N | Old-version rollback | Reintroducing a previously valid but revoked artifact, signature, measurement, or reference set is denied. |

### Leakage and audit-residue tests

Use unique high-entropy markers for customer data, JIT credentials, LWKs,
encrypted release payloads, and tenant identity. Scan byte, hex, base64,
JSON-escaped, URL-encoded, and common truncated forms.

| ID | Lane | Test | Pass condition |
| --- | --- | --- | --- |
| LEK-001 | R,C | Worker filesystem scan | Markers are absent from host filesystems, `/tmp`, state directories, logs, package caches, and shell/process artifacts. |
| LEK-002 | R,C | Storage scan | Markers are absent from raw zvols, ZFS snapshots, pool slack where practically inspectable, send streams, and backups. |
| LEK-003 | R,N | Process interface scan | Markers are absent from worker argv, environment, `/proc`, QMP, systemd metadata, coredumps, and profiles. |
| LEK-004 | R,C | Observability scan | Markers are absent from VictoriaLogs, VictoriaMetrics labels, ClickHouse events/traces, Alerta, audit logs, and test reports. |
| LEK-005 | R,N | Console scan | Markers are absent from serial, OVMF, kernel, initramfs, guestd, OpenZFS, and QEMU logs including panic paths. |
| LEK-006 | R | Verifier privacy | Evidence and results disclose only approved platform/measurement identifiers, are access-controlled, and use a documented retention period. |
| LEK-007 | R | Audit integrity | Worker deletion or fabrication cannot remove or forge remote release/promotion records without detection. |
| LEK-008 | E | Metadata characterization | Measure visible sizes, timing, I/O, endpoints, reuse patterns, and platform identifiers; customer documentation exactly matches observed disclosure. |

### Operational recovery and incident drills

| ID | Lane | Test | Pass condition |
| --- | --- | --- | --- |
| OPS-001 | R,Q | Emergency TCB revocation | Operators can deny a vulnerable TCB/measurement quickly, observe affected jobs/nodes, and retain evidence without releasing new keys. |
| OPS-002 | R,Q | Firmware rollout | Roll forward and rollback across firmware/microcode changes preserve deliberate reference values and never silently broaden admission. |
| OPS-003 | N,Q | AMD KDS outage | Cached endorsements obey freshness policy; jobs fail predictably when the safe cache horizon expires. |
| OPS-004 | N,Q | Trustee/OpenBao outage | Confidential jobs fail or queue within bounds; no worker-local bypass or cached plaintext key appears. |
| OPS-005 | Q | Key-system disaster recovery | Restore from off-worker backups preserves least privilege, audit history, and exact wrapped keys without universal emergency credentials on workers. |
| OPS-006 | Q | Management-plane compromise analysis | Exercise revocation and rebuild of Trustee/OpenBao/control-plane identities; document which customer claims fail under this root compromise. |
| OPS-007 | N,Q | Worker compromise | Assume root compromise, evacuate/deny the node through declared control-plane state, preserve remote evidence, rebuild it, and prove no customer key rotation is required absent a TEE failure. |
| OPS-008 | Q | Key compromise | Revoke and rotate Trustee/OpenBao/control-plane keys, identify affected lineages and releases, and prevent new use of compromised material. |
| OPS-009 | C | Security canary alerting | Attestation denial, key-release denial, encrypted restore, seal, residue, and latency failures alert with customer-impact context and no secrets. |
| OPS-010 | Q | Evidence production | Produce a customer/auditor bundle for a synthetic job showing artifact provenance, admitted measurement/TCB, policy/reference digests, decision, generation lineage, and destruction evidence. |

### External assessment campaigns

| ID | When | Scope |
| --- | --- | --- |
| EXT-001 | Before first customer and material redesign | Architecture and threat-model review, including whether the worker trust posture and customer claims are internally consistent. |
| EXT-002 | Before first customer and protocol changes | Attestation transcript, secure channel, key hierarchy, release policy, generation freshness, and cryptographic-erasure review. |
| EXT-003 | Before first customer and major Trustee/OpenBao updates | Source audit of evidence parsing, certificate/TCB validation, reference appraisal, resource authorization, OpenBao integration, cache, and admin APIs. |
| EXT-004 | Before first customer and annually | Malicious-worker red team with root, QEMU/QMP modification, storage and network interception, clock/device manipulation, and physical console/BMC access. |
| EXT-005 | Before first customer and annually | Malicious-customer red team with hostile kernel, QEMU/KVM/device fuzzing, network attacks, exhaustion, and cross-tenant attempts. |
| EXT-006 | Before claims about side channels | Side-channel assessment tailored to the actual customer workload classes and SMT/device policy. |
| EXT-007 | Before first customer and major build changes | Supply-chain assessment from source review through artifact signing, measurement publication, reference rollout, and emergency revocation. |
| EXT-008 | Every campaign | All critical/high fixes are independently retested; medium residual risks have an owner, deadline or explicit acceptance, and customer-claim impact. |

## Security tooling and implementation shape

Prefer upstream components and existing fault facilities over a Guardian-only
security protocol:

- Use Confidential Containers Trustee v0.19.0 KBS, Attestation Service, and
  RVPS. Guardian supplies restrictive SNP appraisal and resource policies,
  active-lease authorization, signed reference values, and negative tests; no
  sample policy or configuration is promoted.
- Use Trustee's built-in OpenBao-compatible Vault KV v1 resource backend with
  certificate verification enabled and a read-only, path-confined token. Key
  creation, rotation, deletion, and backup use a separate control-plane
  identity and the existing OpenBao recovery doctrine.
- Use pinned [`snphost`](https://github.com/virtee/snphost) and the
  [`sev` crate](https://github.com/virtee/sev) as the independent host probe and
  report-vector verification implementation.
- Reuse upstream
  [QEMU fuzz infrastructure](https://www.qemu.org/docs/master/devel/testing/fuzzing.html),
  KVM selftests, [kvm-unit-tests](https://gitlab.com/kvm-unit-tests/kvm-unit-tests),
  and [syzkaller](https://github.com/google/syzkaller) rather than inventing
  VMM fuzzers.
- Use kernel/device fault facilities such as `dm-flakey`, `dm-error`,
  `dm-delay`, network namespaces, nftables, and `tc netem` for deterministic
  storage/network faults. Any extra proxy is thin test glue with no production
  protocol role.
- Use the exact pinned OpenZFS v2.4.3 suite plus targeted encrypted-pool import, rewind,
  corruption, replay, crash, clone, and outer-send/receive cases. Do not build a
  Guardian-only encrypted block format or freshness tree.

Extend the existing Postflight hammer rather than build an unrelated test
control plane. The security battery needs logical commands for:

```text
attestation-vectors
key-release-negative
storage-faults
hostile-worker
guest-escape
residue-scan
incident-drill
evidence-report
```

The harness may request declared scenarios and collect evidence. Persistent
node configuration, policy, and deployment changes continue to enter through
reviewed Git and converge through Flux.

## Evidence bundle

Every hardware-conformance, hostile-worker, and release run produces a signed
bundle containing:

- Guardian revision and Bazel target identities;
- hardware family/model and pseudonymous platform identity;
- BIOS, microcode, SNP firmware, host kernel, QEMU, OVMF, guest kernel,
  initramfs, dm-verity root, guestd, OpenZFS, Trustee, OpenBao, and
  policy/reference versions;
- artifact digests, signatures, provenance, SBOMs, expected measurement, and
  observed measurement;
- sanitized attestation evidence, endorsement/CRL cache versions, result,
  policy digest, reference-set digest, and release decision;
- lineage, generation, parent, marker digest, remote CAS result, and
  destruction/reap evidence for synthetic scopes;
- per-test result, timing, injected fault, expected outcome, actual outcome,
  and links to secret-scanned logs/traces;
- unresolved findings, severity, owner, due date or risk acceptance, and
  affected customer claim.

The evidence store is remote from the worker, append-only or tamper-evident,
retained according to the customer/audit policy, and contains no customer
secret, plaintext LWK, or reusable release payload.

## Release acceptance

A confidential worker tuple is eligible for customer jobs only when:

1. all **R** tests applicable to its design pass;
2. the latest hostile-worker battery passes and is within its freshness SLO;
3. the continuous canary is healthy;
4. there are no unresolved critical or high findings;
5. every accepted medium finding names the affected claim and has an owner and
   explicit disposition;
6. the external architecture, protocol, malicious-worker, and VM-isolation
   assessments have passed for the first customer release;
7. all admitted measurements, minimum TCBs, policies, and references are
   present in the signed release manifest;
8. customer-facing security language matches C1–C10 and the non-claims in this
   document.

An expired, missing, or failing evidence bundle removes the tuple from
customer-bearing scheduling. It does not create an operator override that
releases keys anyway.

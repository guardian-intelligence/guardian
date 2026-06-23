Bazel polyglot hermetically sealed monorepo for Guardian, a free open-source self-hostable private cloud capable of selling excess compute as QEMU sandboxes. Early days, still getting the infra set up.

Pitch: CozyStack for agents.

Reference Cozystack for prior art for the cloud portion.

Default release channels: Edge (CD on main), nightly, RC, stable.
Default deployment stages: dev (PR preview), gamma (staging), prod.

IOW: Bazel owns the build graph and produces bytes using OCI for layout. `cosign`/SLSA proves its authentic Guardian Intelligence LLC software. Cozystack management cluster reconciles our declared state.

Grep through docs https://github.com/cozystack/cozystack/tree/release-1.5.0/docs (ensure you're reading the 1.5 docs and NOT v0 docs or v1.4.0.)

Domain: guardianintelligence.org (abbreviated gi.org)

See ~/Projects/verself-sh for reference https://github.com/guardian-intelligence/verself which was a Nomad-based version of this approach.

Objectives:

We're maximizing for safe operations (disaster-recovery from wiped box + offsite backups as priority 1, behind ongoing security checks + hardening) and highly continuous rapidly delivered software to external vendors like NPM/PyPi/Crates.io and so on.
After doing some financial calculation I also realize I need to make provisioning N workload nodes (rs4.metal.xlarge CPU: AMD 9554P, 64 Cores @ 3.1 GHz / RAM: 1.5 TB / Storage: 2 x 480 GB NVME + 4 x 8 TB NVME / NIC: 2 x 100 Gbps) a first class concept as well, otherwise we don't break even.

Important context:
- All dependencies version/commit pinned. Nothing during runtime, dev time, test time, or build time should require external non-version-pinned tooling, or shell out to binaries outside this repo or its build artifacts.
- Dev tools: `aspect`. Run `aspect tidy` to format the codebase.
- Don't use CUE. Avoid custom schemas, protocols, shell scripts, contracts.
- API IDL in Protobuf/Connect. Define IAM, audit, risk, request-size, rate limit, and idempotency metadata as explicit operation policy on the RPC contract.
- Protobuf governance uses the repo-pinned Buf toolchain through Bazel: linting, formatting, and breaking-change checks run from `rules_buf`; code generation uses local pinned generators only. Do not use Buf remote plugins in build/test/release paths.
- All operations must run unattended, no human-in-the-loop.
- Invent nothing. If we write our own code, it should be glue code over existing libraries and apeing reference implementations of solutions to problems only. Always do the boring industry-standard thing.
- Code is not the truth for how the system works. Traces are.
- Use SQLC.
- Do not provide time estimates.

Constraints:
- Secrets must be autoprovisioned/autorotated.
- Structure infra for BYOC on-prem
- Traces are the only admissable proof -- ClickHouse (when stood up), Victoria Metrics, Victoria Logs. Collect traces/spans and relevant log lines to support your thesis that your task is complete to satisfaction. Test services under heavy load via k6 to surface subtle bugs.

Service architecture:

- Cozystack
- Guardian distinguishes distributed software from deployed software. Distributed software runs on the user's device; deployed software runs on Guardian-owned capacity. Promotions between release channels will be done through Kargo. Promotions for deployments will be done through Flux.
- Releasing distributed software is a one-way door: after a CLI, SDK, crate, wheel, or desktop/mobile artifact is public, rollback means publishing a new artifact and helping consumers move. Its gates must get stricter as it approaches stable.

Sandbox isolation doctrine (multi-tenant QEMU), the below is advisory for when we stand up our QEMU warm pool:

- Trust model: every sandbox guest is hostile and may hold a kernel exploit. The isolation boundary is KVM plus a jailed VMM, never namespaces alone — containers share a kernel and a kernel escape is a fleet escape. Untrusted-code planes (customer CI, agent sandboxes) run only on workload nodes, never on control-plane or product hosts.
- The bar: meet AWS Firecracker's software-isolation posture, minus hardware confidential computing. We do not yet have SEV-SNP/TDX encrypted memory or TPM-attested guests (SEV is off in BIOS on the f4 boxes; the rs4 plane is unverified), so memory-in-use confidentiality against a malicious host/operator is an accepted gap, deferred until the Confidential Computing Consortium path matures and we enable TEE on the rs4 workload nodes (Phase 4). Until then the host is trusted; tenants are isolated from each other, not from us. This is runtime memory confidentiality, distinct from build/release attestation (which needs no TEE).
- VMM substrate: QEMU/KVM with `-cpu host`; q35 only where a feature requires it (virtio-mem hotplug needs ACPI/PCI), otherwise prefer `-machine microvm`. Keep cloud-hypervisor (Rust, memory-safe, VFIO-capable) as the standing drop-in alternate VMM target — the jail and network model below are VMM-agnostic.
- Device-surface minimization: virtio-only (virtio-blk, virtio-net, virtio-serial/vsock). `-nodefaults`, `-no-user-config`, `-nographic`. No USB, floppy, CD, audio, or emulated legacy NIC/block. Patch QEMU device-emulation CVEs on the critical path — the standing tax of a C VMM.
- VMM process jail: each QEMU runs (1) as a dedicated unprivileged uid/gid, never root — the worker creates the TAP, then the process drops privilege; (2) under `-sandbox on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny` plus a supervisor seccomp-bpf whitelist with no_new_privs; (3) in its own user/mount/net/pid namespaces, pivot_root'd into a minimal rootfs holding only its drives and sockets; (4) under a per-VM AppArmor/SELinux (sVirt-style) label so an escaped QEMU cannot reach another tenant's disk; (5) in a cgroup v2 slice with memory.max/high, cpu.max, pids.max, io.max — PSI on the slice is the host saturation signal.
- Network isolation: one TAP + dedicated netns per VM, default-deny egress with explicit NAT, anti-spoof nftables on MAC+IP, per-VM bandwidth cap, no inter-VM L2 reachability, metadata reachable only at our controlled link-local endpoint. Ape OpenComputer's per-VM /30 but terminate it in a netns, not a shared bridge.
- Memory density without cross-tenant leakage: no cross-tenant KSM (write-timing side channel + Flip Feng Shui Rowhammer). Golden-image RAM savings come from a read-only shared base image (page-cache shared, never written) plus a per-sandbox COW overlay (ZFS clone) — only known-public content is ever shared. The density multiplier is hibernation/oversubscription of idle VMs, which shares nothing live.

Release doctrine:

- A release is a typed operation over a release target, not a workflow file. The release target is the tuple of distributable, source commit, package/ecosystem version or coordinate, publisher, platform, build flavor, and channel intent.
- In OCI artifact paths, `npm` means the npm package/tarball format, not npmjs.com as publisher; npmjs publication is a downstream projection.
- Promotion preserves source lineage and release intent. Bit identity is package/ecosystem-specific: OCI images may promote the same digest, while npm/crates/iOS/Electron may rebuild because version, signing, or channel metadata changes bytes.
- Inside the Bazel monorepo, the only internal version is the commit. Ecosystem versions, dist-tags, app versions, OCI tags, and channel pointers are external projections of a commit and artifact evidence; Guardian code must not depend on internal semver between repo modules.
- Package-owned release tooling owns release semantics: version derivation, release notes, build targets, supported platforms/flavors/publishers, publisher-specific packaging, and retry behavior. Shared release infrastructure owns source resolution, subject normalization, provenance shape, signing hooks, idempotency, audit events, and result records.
- `aspect release ...` is the durable operator surface and should stay thin. It builds/runs the package release binary and passes flags through; it does not encode package policy itself.
- Every release target must be idempotent and retryable. If an external artifact/version already exists with matching bytes or digest, no-op; if it exists with different bytes, fail loudly. Partial release runs must resume by verifying already-published targets before continuing.
- API operation policy should live with the operation contract where practical: auth, audit level, risk tier, request body limit, rate limit, and idempotency requirements should be generated into Connect/Go interceptors or equivalent boring enforcement code.
- Do not hand-roll a release promotion product. Target Kargo for distributable promotion graphs, staged gates, approvals, verification, and release-train visibility. Guardian-owned release code should be limited to small policy/verifier commands that check cosign signatures, SLSA/in-toto attestations, expected builder identity, subject digest, source commit, package/version, required gate attestations, and taint/rejection status.
- Target Flagger for deployed-service progressive delivery after Flux applies an approved workload digest: canary, blue/green, metric checks, webhooks, promotion, and rollback. Flagger is not the distributable promotion system.
- Release channels and stage names are not the same thing. Distributed software advances through increasing evidence gates such as edge -> nightly -> rc -> stable. Deployed software advances through runtime environments and rollout strategies such as dev/gamma/prod plus canary or blue/green.

Fleet (all Latitude.sh ASH, f4.metal.small; the Latitude project is `guardian`;
per-box physical facts — MACs, disk serials, gateways — live in
`src/hosts/<asset-id>/host.yaml`; post-Kubernetes desired state lives in
`src/environments/<environment>/environment.yaml`). Physical hostnames are
stable asset names, not stage names. Dev/gamma/prod are assignments expressed
by clusters, namespaces, environments, and tags.

Compute doctrine (ratified 2026-06-12): all Verself compute is subsumed — the
whole fleet is guardian fleet, one pool. Spare dev/staging capacity IS workload
capacity: idle boxes run customer workloads instead of sitting as cold spares.
Customer/untrusted work never runs as a k8s pod — it runs in QEMU microVMs
drawn from a per-host **warm pool** (booted before the work exists; "launch" =
bind a late-loaded rootfs to a waiting VM, which is what makes start time
constant and placement locality-free). The pool is owned by a single
**workload-agent binary**: hosts know nothing except "run the agent"; the agent
boots/recycles VMs, binds work, supervises the full lifecycle, and heartbeats
capacity. Kubernetes schedules the *agent*, not the sandboxes — sandbox
placement is verself-v2 control-plane logic (power-of-two-choices over
eventually-consistent capacity heartbeats; lock-free, tolerant of stale state).
QEMU over Kata: Kata would put the k8s scheduler in the sandbox path and boot
VMs at pod-create (defeating the warm pool); plain QEMU keeps the agent in
charge and the PCI/TEE/GPU passthrough path open for the rs4 era. Agent
upgrades ship like any release: quiesce is a first-class verb (drain a host's
pool without disrupting running work), and load tests across the nonprod fleet
gate promotion.

| Layer | Facts |
| - | - |
| Provider | Latitude.sh bare metal. Rescue mode is the reliable recovery lever (~3 min). OS-of-record governs the boot chain: `ubuntu` chainloads the local disk; `ipxe` chains the URL on every boot and disables rescue. Reinstall workflows wedge when the box is dark; power-cycle kicks them. |
| BMC / IPMI (not ours) | Supermicro BMC, SOL payload on channel 1 port 623. **2026-06-11: operator confirms IPMI access, OOB access, and SOL are all available via the Latitude API** — this is the true out-of-band disaster-recovery path (survives any host-side mistake: NIC misconfig, firewall lockout, dead CNI), to be verified working before any host-firewall enforcement. (Historical, 2026-06-10: the per-user SOL payload for Latitude's OOB proxy user (7, `customer_access`) measured disabled in-band; superseded by the API-side access above.) Kernel side handled: schematic bakes `console=ttyS1,115200n8`. OOB SOL proxy: `POST /servers/{id}/out_of_band_connection`. |
| Hardware | Supermicro AS-3015MR-H10TNR, AMD EPYC 4484PX (Zen 4, 12c). 2× 894GiB NVMe — select by serial, device names swap across boots. TPM 2.0 discrete (Infineon IFX). AMD-Vi IOMMU. PSP/CCP present (`psp enabled`, `tee enabled`) but SEV/SEV-SNP off in BIOS (no CPU flags, no `/dev/sev`); BIOS access via provider required to change. |
| Firmware | AMI UEFI 2.5. Matrix is UEFI-only; Secure Boot off today (UKI signing + TPM measurement is the planned path). |
| OS | Talos Linux v1.13.0 (Cozystack 1.5.0 talm-preset set): immutable, API-only, no SSH. Machine config generated by `guardian up` from `host.yaml` (serial-selected install disk, static network, single-node patches). |
| Disks | System NVMe — drills wipe STATE+EPHEMERAL only (`--wipe-mode all` erases the bootloader and user disks). Data NVMe — reserved for ZFS, survives `down`. |
| Cluster | Kubernetes v1.34.3, single node, default CNI, PSA baseline with privileged namespaces opt-in per component. |
| Secrets | OpenBao v2.5.4, raft integrated storage, sealed by default. Backup writes know R2; restore takes `(blob, sha256)`; init/unseal/restore are operator decisions, never automated. |
| Build | Bazel 9.1.0 (bazelisk sha256-pinned), bzlmod, rules_oci/rules_go; Go 1.26.4; deterministic OCI layouts and tarballs. |

Product Surfaces:

- GitHub App (20x faster CI than GitHub Actions). (Not Yet Implemented, must be adapted from Verself repo)
- Software Company from an API call or web surface; the CLI only prepares hosts for the cluster control plane. (Not Yet Implemented)

Objectives (outdated but mildly useful historical context)

Phase 1 - Lay the groundwork: "the goal is "You just provisioned a box on latitude. Run the guardian CLI from your laptop to turn it into a functional Guardian cluster that can hand off to Kubernetes/Crossplane/Flux" (we'll get as fast as physically possible without a warm pool, and then do a warm pool + managed/billed approach to get it under 4 minutes). We ship this host-bootstrap capability via the `guardian` CLI, which is also what we dogfood to provision OUR own servers and execute disaster-recovery drills (required). We're out of this phase when we have oci.guardianintelligence.org stood up vending in-toto attested binaries that users can run `cosign verify` on. No TEE because all code is open-source. See https://github.com/guardian-intelligence/verself/pull/150 for a previous attempt. This also doubles as our own disaster recovery procedure, which we will execute on an hourly basis.

Phase 2 - We assimilate the gamma and prod boxes from Verself and then figure out a GitOps pipeline: development boxes ship a single-box version of Guardian. Merges to main continuously deploy to Gamma. Synthetics canaries continuously run against all environments. On Gamma they gate promotion to Prod. on Prod they trigger alerts/rollbacks. This is the critical phase. We get confidence in our release process and then we rapidly release software, create a pipeline to automate announcements to the guardianintelligence.org/news, begin getting publicity, traction, contributing useful free open source software. Gate: we have automated or nearly automated releases/yank-drills practiced for

Phase 3 - We figure out how to be a real cloud, provisioning capacity ahead of expected demand.

Phase 4 - The fun part, we rapidly build feature parity with Verself. Starting with "Sign in with GitHub" and onboard our first customer. We'll be done with this phase and on to building new features once we have TEE on the rs4.xlarge workload nodes for customer CI.

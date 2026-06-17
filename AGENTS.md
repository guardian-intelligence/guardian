Bazel polyglot hermetically sealed monorepo for Guardian, a free open-source self-hostable cloud. The `guardian` CLI owns host lifecycle: stock Ubuntu or Talos maintenance -> Talos/Kubernetes bootstrap substrate. Kubernetes, Crossplane, Flux, and release tooling own runtime desired state.

Domain: guardianintelligence.org (abbreviated gi.org)

Optimize for BYOC on-prem

See ~/Projects/verself-sh for reference https://github.com/guardian-intelligence/verself which was a Nomad-based version of this approach.


Objectives:

We're maximizing for safe operations (disaster-recovery from wiped box + offsite backups as priority 1, behind ongoing security checks + hardening) and highly continuous rapidly delivered software to external vendors like NPM/PyPi/Crates.io and so on.
After doing some financial calculation I also realize I need to make provisioning N workload nodes (rs4.metal.xlarge CPU: AMD 9554P, 64 Cores @ 3.1 GHz / RAM: 1.5 TB / Storage: 2 x 480 GB NVME + 4 x 8 TB NVME / NIC: 2 x 100 Gbps) a first class concept as well, otherwise we don't break even.

Important context:
- Source: `src/crossplane/`, `src/sites/`.
- All dependencies version/commit pinned. Nothing during runtime, dev time, test time, or build time should require external non-version-pinned tooling, or shell out to binaries outside this repo or its build artifacts.
- The `guardian` CLI is not a dumping ground for generic functionality. Its sole purpose is to manage host come-up.
- Dev tools: `aspect`. Run `aspect tidy` to format the codebase.
- 1p configuration schemas in CUE, always. Read/Render-out YAML/JSON/TOML. Output must support all 3.
- API IDL in Protobuf/Connect. Define IAM, audit, risk, request-size, rate limit, and idempotency metadata as explicit operation policy on the RPC contract.
- Protobuf governance uses the repo-pinned Buf toolchain through Bazel: linting, formatting, and breaking-change checks run from `rules_buf`; code generation uses local pinned generators only. Do not use Buf remote plugins in build/test/release paths.
- All operations must run unattended, no human-in-the-loop.
- Invent nothing. Glue code over existing libraries and aping reference implementations of solutions to problems only. Always do the boring industry-standard thing. We are modeling our approach after the Zarf/UDS/Defense Unicorns "air-gapped seed" pattern, but we know the machine we're deploying to and we control more layers.
- Code is not the truth for how the system works. Traces are.
- Use SQLC.
- Do not provide time estimates.

Constraints:
- Installing dependencies when building from source is OK. Doing so on a traffic-serving host (prod/gamma/dev et. al) is not. Traffic-serving hosts use a commit-pinned release artifact of this repo.
- We don't do sidecars.
- ClickHouse wide events / Observability 2.0. We don't separate metrics, logs, and traces. Time series data lives in ClickHouse as Wide Events except for float values that we care about for monitoring which go into VictoriaMetrics
- Cross-site isolation. 
- Secrets must be autoprovisioned/autorotated. Use OpenBao as the source of truth and K8s Secrets as the delivery mechanism.

Service architecture:

- One control plane: Kubernetes + the guardian reconcilers/operators (CRDs like a future `SoftwareCompany`/`WorkloadNode`). Individual services do NOT each get their own control-plane/data-plane — that's rebuilding K8s inside a service. The CP/DP split exists once, at the platform level, and we express "control plane" as the operator pattern (CRD + controller), which maps onto the Protobuf/Connect contract for IAM/audit.
- Default to a module behind a Protobuf/Connect contract, not a service. Enforce module boundaries with Bazel visibility so the module is the staging ground for a future service: it lifts out into its own Deployment cheaply because its contract was already explicit.
- Promote a module to its own Deployment the moment it earns it on one axis: independent rollout cadence, a different scaling profile, a trust boundary (internet-facing or runs untrusted code, e.g. CI), or hard resource needs (GPU, the 1.5 TB box, TEE). Otherwise it stays a module.
- The floor on "smaller" is the bounded context / control loop that owns its own data. Never split two things that change together — that's a distributed monolith (every cost of services, every cost of the monolith, deploys slower and riskier than either).
- Guardian legitimately runs smaller/more services than a normal app: it's a platform of control loops (one per capability), and SPIRE (identity) + Protobuf/Connect (contracts/audit) + Bazel (hermetic builds) pre-pay the per-service tax that usually makes microservices too expensive. Cheaper, not free — every service is still its own SLO, on-call surface, and data-ownership decision.
- Webhook handlers (Stripe, GitHub) are dumb edge adapters, not services: verify signature, persist the raw event idempotently (redelivery happens), ack 200 fast, hand off for processing. Default to a route in the app; break into its own pod only to keep ingestion alive across an app deploy, and even then the processing logic does not move out with it.

Release doctrine:

- A release is a typed operation over a release target, not a workflow file. The release target is the tuple of distributable, source commit, package/ecosystem version or coordinate, publisher, platform, build flavor, and channel intent.
- In OCI artifact paths, `npm` means the npm package/tarball format, not npmjs.com as publisher; npmjs publication is a downstream projection.
- Promotion preserves source lineage and release intent. Bit identity is package/ecosystem-specific: OCI images may promote the same digest, while npm/crates/iOS/Electron may rebuild because version, signing, or channel metadata changes bytes.
- Inside the Bazel monorepo, the only internal version is the commit. Ecosystem versions, dist-tags, app versions, OCI tags, and channel pointers are external projections of a commit and artifact evidence; Guardian code must not depend on internal semver between repo modules.
- Package-owned release tooling owns release semantics: version derivation, release notes, build targets, supported platforms/flavors/publishers, publisher-specific packaging, and retry behavior. Shared release infrastructure owns source resolution, subject normalization, provenance shape, signing hooks, idempotency, audit events, and result records.
- Workflow YAML is only an executor shim: checkout, obtain repo-pinned tools, and run an Aspect task. Release decisions, matrices, publisher fan-out, signing, attestation, verification, and no-op logic belong in purpose-built Go binaries executed through `aspect`.
- `aspect release ...` is the durable operator surface and should stay thin. It builds/runs the package release binary and passes flags through; it does not encode package policy itself.
- Every release target must be idempotent and retryable. If an external artifact/version already exists with matching bytes or digest, no-op; if it exists with different bytes, fail loudly. Partial release runs must resume by verifying already-published targets before continuing.
- Gate results are first-class artifacts. Synthetic checks and SLO evaluation should emit machine-readable gate-result records; signing them as in-toto/SLSA-style attestations is the natural next step, not a bespoke release ledger.
- Distribution/admission services, when present, admit immutable digests, verify standard OCI/in-toto/SLSA evidence, gate public reads, and move channel pointers. They do not build artifacts and they do not know package-specific Bazel labels.
- API operation policy should live with the operation contract where practical: auth, audit level, risk tier, request body limit, rate limit, and idempotency requirements should be generated into Connect/Go interceptors or equivalent boring enforcement code.

Fleet (all Latitude.sh ASH, f4.metal.small; per-box physical facts — MACs, disk
serials, gateways — live in `src/sites/<site>/bootstrap.yaml`; post-Kubernetes
desired state lives in `src/crossplane/environments/<site>/environment.yaml`.
Physical facts are derived from the box and never copied between boxes: prod's
external NIC is X550 fn 0 where dev/gamma use fn 1):

| Site | Hostname | IP | Latitude ID | Serves | Notes |
| - | - | - | - | - | - |
| dev | vs-dev-w0 | 206.223.228.101 | sv_vAPXaMxKM5epz | dev.aisucks.app | cluster `guardian-dev`; host bootstrap/drill surface: `guardian up src/sites/dev/bootstrap.yaml` |
| gamma | gd-gamma-w0 | 45.250.254.119 | sv_nPRbajqEB5koM | gamma.aisucks.app | cluster `guardian-gamma`; release gate (canary submissions); monthly billing |
| prod | gd-prod-w0 | 67.213.115.113 | sv_BDXM5E4QLNrpk | aisucks.app | cluster `guardian-prod`; reserved/yearly billing (support ticket open to convert) |

Verself boxes (run live Verself under Nomad — ClickHouse, TigerBeetle,
Temporal): subsumption is the ratified direction (Compute doctrine below);
per-box status differs:

- `vs-gamma-w0` (206.223.228.87, sv_8mop5gZo8Njxv): **RELEASED for takeover**
  — explicit operator go 2026-06-12, including the data: the 384GB Verself
  dataset on it is discardable (operator confirmed Verself keeps its own
  backups in a separate R2 bucket). Wipe and enroll freely; converge its
  boot chain to the gd-style ipxe OS-of-record when enrolling.
- `vs-prod-w0` (206.223.228.99, sv_EvjLaBxRQNoqy): NOT ours to touch yet.
  Stays Verself prod until a separate explicit go. Never wipe, reinstall, or
  reconfigure it.

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

Offsite survival floor: the cluster CA roots (`~/.local/state/guardian/`) and the
prod corpus live age-encrypted in the R2 bucket `guardian-vault` (decryption
identity only in the operator's sops store; R2-flow token trio in gitignored
`secret.env` — account-owned Cloudflare tokens cannot reach R2's data plane);
procedure, measured auth matrix, and the drilled-restore record:
`docs/runbooks/survival-floor.md` (roadmap: `docs/roadmap.md` M0).

vs-dev-w0 Latitude ASH

| Layer | Facts |
| - | - |
| Provider | Latitude.sh bare metal. Rescue mode is the reliable recovery lever (~3 min). OS-of-record governs the boot chain: `ubuntu` chainloads the local disk; `ipxe` chains the URL on every boot and disables rescue. Reinstall workflows wedge when the box is dark; power-cycle kicks them. |
| BMC / IPMI (not ours) | Supermicro BMC, SOL payload on channel 1 port 623. **2026-06-11: operator confirms IPMI access, OOB access, and SOL are all available via the Latitude API** — this is the true out-of-band disaster-recovery path (survives any host-side mistake: NIC misconfig, firewall lockout, dead CNI), to be verified working before any host-firewall enforcement. (Historical, 2026-06-10: the per-user SOL payload for Latitude's OOB proxy user (7, `customer_access`) measured disabled in-band; superseded by the API-side access above.) Kernel side handled: schematic bakes `console=ttyS1,115200n8`. OOB SOL proxy: `POST /servers/{id}/out_of_band_connection`. |
| Hardware | Supermicro AS-3015MR-H10TNR, AMD EPYC 4484PX (Zen 4, 12c). 2× 894GiB NVMe — select by serial, device names swap across boots. TPM 2.0 discrete (Infineon IFX). AMD-Vi IOMMU. PSP/CCP present (`psp enabled`, `tee enabled`) but SEV/SEV-SNP off in BIOS (no CPU flags, no `/dev/sev`); BIOS access via provider required to change. |
| Firmware | AMI UEFI 2.5. Matrix is UEFI-only; Secure Boot off today (UKI signing + TPM measurement is the planned path). |
| Boot | systemd-boot + UKI from the Image Factory schematic (`src/sites/<site>/talos/schematic.yaml`): ZFS extension and static `ip=` baked in, content-addressed by schematic ID. Kernel cmdline lives inside the UKI (`machine.install.extraKernelArgs` must stay unset). |
| OS | Talos Linux v1.13.4: immutable, API-only, no SSH. Machine config generated by `guardian up` from `bootstrap.yaml` (serial-selected install disk, static network, single-node patches). |
| Disks | System NVMe — drills wipe STATE+EPHEMERAL only (`--wipe-mode all` erases the bootloader and user disks). Data NVMe — reserved for ZFS, survives `down`. |
| Cluster | Kubernetes v1.36.1, single node, default CNI, PSA baseline with privileged namespaces opt-in per component. |
| Artifacts | In-cluster seed registry (CNCF `registry:3`, digest-pinned, hostPath-persistent) behind the `registry.guardian.internal` mirror; workspace-built OCI layouts pushed by digest over a port-forward. What runs is byte-for-byte what the workspace built. |
| Secrets | OpenBao v2.5.4, raft integrated storage, sealed by default. Backup writes know R2; restore takes `(blob, sha256)`; init/unseal/restore are operator decisions, never automated. |
| Identity | SPIRE — planned. |
| Control | `guardian` CLI: `up`, `down --yes`, `config bootstrap` for host/bootstrap lifecycle only. talosctl v1.13.4 and kubectl v1.36.1 ride in runfiles; per-cluster state in `~/.local/state/guardian/<cluster>/`. |
| Build | Bazel 9.1.0 (bazelisk sha256-pinned), bzlmod, rules_oci/rules_go; Go 1.26.4; deterministic OCI layouts and tarballs. |
| Release | Planned: signed release manifests (component→digest sets), channels as signed pointers, zot, cosign via OpenBao Transit, npm projection of the CLI via dist-tags. |
| Clients | Planned: web client and CLI binaries distributed through npm under the guardian-intelligence org. |

Product Surfaces:

- GitHub App (20x faster CI than GitHub Actions). (Not Yet Implemented)
- Software Company from an API call or web surface; the CLI only prepares hosts for the cluster control plane. (Not Yet Implemented)

Current Objectives

Phase 1 - Lay the groundwork: "the goal is "You just provisioned a box on latitude. Run the guardian CLI from your laptop to turn it into a functional Guardian cluster that can hand off to Kubernetes/Crossplane/Flux" (we'll get as fast as physically possible without a warm pool, and then do a warm pool + managed/billed approach to get it under 4 minutes). We ship this host-bootstrap capability via the `guardian` CLI, which is also what we dogfood to provision OUR own servers and execute disaster-recovery drills (required). We're out of this phase when we have oci.guardianintelligence.org stood up vending in-toto attested binaries that users can run `cosign verify` on. No TEE because all code is open-source. See https://github.com/guardian-intelligence/verself/pull/150 for a previous attempt. This also doubles as our own disaster recovery procedure, which we will execute on an hourly basis.

Phase 2 - We assimilate the gamma and prod boxes from Verself and then figure out a GitOps pipeline: development boxes ship a single-box version of Guardian. Merges to main continuously deploy to Gamma. Synthetics canaries continuously run against all environments. On Gamma they gate promotion to Prod. on Prod they trigger alerts/rollbacks. This is the critical phase. We get confidence in our release process and then we rapidly release software, create a pipeline to automate announcements to the guardianintelligence.org/news, begin getting publicity, traction, contributing useful free open source software. Gate: we have automated or nearly automated releases/yank-drills practiced for

Phase 3 - We figure out how to be a real cloud, provisioning capacity ahead of expected demand.

Phase 4 - The fun part, we rapidly build feature parity with Verself. Starting with "Sign in with GitHub" and onboard our first customer. We'll be done with this phase and on to building new features once we have TEE on the rs4.xlarge workload nodes for customer CI.

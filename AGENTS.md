Bazel polyglot hermetically sealed monorepo for Guardian, a free open-source self-hostable private cloud capable of selling excess compute as QEMU VMs. Early days, still getting the infra set up.

Pitch: CozyStack for agents.

Grep through docs https://github.com/cozystack/cozystack/tree/release-1.5.0/docs (ensure you're reading the 1.5 docs and NOT v0 docs or v1.4.0.)

Reference Cozystack for prior art for the cloud portion. Other inspiration: Zarf/UDS, AWS Landing Zone Accelerator
<company_topology>
Single Global Writer
Public DNS stays globally managed, for now.

Cozystack tenants map mostly onto AWS Accounts. `tenant-root` is the required Cozystack root/admin tenant for a regional management cluster. Cozystack packages/operators, Flux handoff, storage classes, COSI/BackupClass/system bucket, root ingress/load-balancer substrate, root infrastructure monitoring, child Tenant CRs, and cluster-wide policy. Do not put product workloads, product databases, or shared business logic in `tenant-root`. tenant-root owns:

- Cozystack Package / operator declarations
- Flux source and Kustomizations that reconcile the management cluster
- child Tenant CRs/Flux slices that create them
- storage substrate: StorageClasses, LINSTOR config, COSI classes
- backup substrate: BackupClass/cozy-default, system bucket plumbing
- root ingress/load-balancer substrate
- MetalLB / Cilium / Gateway substrate if cluster-scoped
- root DNS/bootstrap glue for cluster entrypoints: guardianintelligence.org
- root Monitoring for Kubernetes/Cozystack/storage/ingress health
- root Harbor/cache only if it is a cluster artifact cache, not a product registry
- bootstrap/break-glass OpenBao only if needed for regional substrate recovery
- cluster-wide NetworkPolicy/RBAC/admission defaults
- cert-manager/issuer substrate if it serves all tenants
- External Secrets / SecretStore bootstrap needed for tenant secret projection
- operational drills for cluster survival: backup restore, node outage, OpenBao unseal, system bucket validation

Today the active region is Latitude ASH (`ash`).

```
region: ash
  cozystack-management-cluster: guardian-mgmt-ash
    tenant-root                     # Cozystack substrate only

    tenant-iam-dev                  # identity, principals, authz policy
    tenant-iam-gamma
    tenant-iam-prod

    tenant-kms-dev                  # OpenBao-backed keys, signing, PKI, secrets
    tenant-kms-gamma
    tenant-kms-prod

    tenant-audit-dev                # CloudTrail-ish audit/event ledger
    tenant-audit-gamma
    tenant-audit-prod

    tenant-telemetry-dev            # CloudWatch/X-Ray-ish operational telemetry
    tenant-telemetry-gamma
    tenant-telemetry-prod

    tenant-release-dev              # artifact admission, Kargo, release evidence
    tenant-release-gamma
    tenant-release-prod

    tenant-billing-dev              # Guardian service
    tenant-billing-gamma
    tenant-billing-prod

    tenant-aisucks-dev              # Guardian product
    tenant-aisucks-gamma
    tenant-aisucks-prod

    tenant-workloads-dev            # QEMU workload-agent fleet, not customer pods
    tenant-workloads-gamma
    tenant-workloads-prod

region: cmh                         # hypothetical future region
  cozystack-management-cluster: guardian-mgmt-cmh
    tenant-root
    tenant-iam-dev
    tenant-iam-gamma
    tenant-iam-prod
    tenant-kms-dev
    tenant-kms-gamma
    tenant-kms-prod
    ...
```

Default release channels: Edge (CD on main), nightly, RC, stable.
Default deployment stages: dev (PR preview), gamma (staging), prod.
</company_topology>

<repo_shape>
The below is the target shape -- repo still in flux and does not match this quite yet

src/
    products/
      company/
        web/
        deploy/base/                   # reusable company website Deployment/
        Service

      aisucks/
        api/
        service/
        web/
        sdk/
        release/
        deploy/base/

    services/
      kms/
        api/                           # future Connect KMS/Secrets API
        service/                       # future wrapper/control plane if needed
        release/
        deploy/base/                   # reusable OpenBao-backed runtime
        component

      release/
        api/
        service/
        release/
        deploy/base/                   # Kargo/admission/registry policy surface

      telemetry/
        api/
        service/
        deploy/base/

    infrastructure/
      components/                      # reusable Kustomize bases/components
        cozystack-root/
        root-monitoring/
        root-harbor-cache/
        openbao-kms/
        harbor-registry/
        postgres-service/
        clickhouse-service/

      clusters/
        ash/
          bootstrap/
            opentofu/
              baremetal/
              dns/
              openbao-bootstrap/
            talm/

          root/                        # reconciled into tenant-root
            kustomization.yaml
            cozystack/
            flux/
            networking/
            storage/
            backup/
            observability/
            registry/
            secrets-bootstrap/
            policy/
            tenants/

          deployments/                 # reconciled into child tenants
            kms/
              dev/
              gamma/
              prod/
            release/
              dev/
              gamma/
              prod/
            company/
              dev/
              gamma/
              prod/
            aisucks/
              dev/
              gamma/
              prod/

      tests/
      cmd/                             # infra validation/drill helpers
      load/
    tools/
</repo_shape>

<technology>
TLS terminates at Cloudflare edge.
Cloudflare LB for the three control plane nodes. [206.223.228.101, 45.250.254.119, 206.223.228.87]
MetalLB for L2/ARP inside the Latitude VLAN. `10.8.0.200 - 10.8.0.240`

Public edge should follow the standard Kubernetes shape wherever the provider
network supports it: `Service.type=LoadBalancer` backed by MetalLB/Cilium
allocation and announcement, with Cloudflare Load Balancing in front for WAF,
TLS, health checks, and failover. The current private MetalLB range is only
reachable on the Latitude VLAN, so it must not be used as a Cloudflare origin.
Using MetalLB as a public origin requires routable service IPs or BGP from the
provider network. Until that exists, Cloudflare origins are the three Latitude
public node IPs, and the public edge must stay stateless so Cloudflare can steer
around unhealthy origins per request.

Bazel owns the build graph and produces bytes using OCI for layout. `cosign`/SLSA proves its authentic Guardian Intelligence LLC software. Cozystack management cluster reconciles our declared state.

Planned: Use Flagger for progressive delivery after Flux applies an approved digest. Use Kargo/Freight to promote immutable release candidates to service+stage+region targets.

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

<development_loop>
- This section is WIP, follow best practices. The below is just a few things to add to normal development workflow
- Do not use CLI commands as a control plane. Rely on flux to converge the cluster on merged commits.
- Run `aspect infra edge-health` to smoke-test edge reachability post convergence. Verify DNS resolution for every configured `guardianintelligence.org` hostname, HTTPS behavior through the public edge and origin consistency checks; the next milestone is moving the same stateless prober to a VPS and integrating it with in-cluster Flagger gates.
- Edge failover drills are single-node exercises. Run the drill once per node by explicit node IP, wait for the node and public edge to recover, document that node's outage window, then move to the next node. A node whose loss breaches 60 seconds of public-edge disruption is load bearing and must be fixed before continuing.
</development_loop>

Constraints:
- Secrets must be autoprovisioned/autorotated.
- Cozystack 1.5 backups use the platform-managed `cozy-default` BackupClass
  and system bucket. Do not add Guardian-specific backup strategies, backup
  credential Secrets, or checks for legacy backup object names.
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

Fleet (all Latitude.sh ASH, f4.metal.small; the Latitude project is `guardian`).

Objectives (outdated but mildly useful historical context)

Planned Product Surfaces:

- GitHub App (20x faster CI than GitHub Actions). (Not Yet Implemented, must be adapted from Verself repo)
- Software Company from an API call or web surface; host come-up tooling only prepares machines for the management cluster. (Not Yet Implemented)


Phase 1 - Lay the groundwork: management-cluster bootstrap is repo-declared infrastructure plus narrowly scoped host come-up tooling. Do not recreate the retired `guardian` CLI as a generic operator surface. Disaster recovery and load testing live as explicit infra drill/load helpers; day-to-day convergence belongs to OpenTofu, Talm, Cozystack, Flux, and standard Kubernetes controllers.

Phase 2 - We assimilate the gamma and prod boxes from Verself and then figure out a GitOps pipeline: development boxes ship a single-box version of Guardian. Merges to main continuously deploy to Gamma. Synthetics canaries continuously run against all environments. On Gamma they gate promotion to Prod. on Prod they trigger alerts/rollbacks. This is the critical phase. We get confidence in our release process and then we rapidly release software, create a pipeline to automate announcements to the guardianintelligence.org/news, begin getting publicity, traction, contributing useful free open source software. Gate: we have automated or nearly automated releases/yank-drills practiced for

Phase 3 - We figure out how to be a real cloud, provisioning capacity ahead of expected demand.

Phase 4 - The fun part, we rapidly build feature parity with Verself. Starting with "Sign in with GitHub" and onboard our first customer. We'll be done with this phase and on to building new features once we have TEE on the rs4.xlarge workload nodes for customer CI.

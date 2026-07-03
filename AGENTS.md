This is a Bazel polyglot hermetically sealed monorepo for Guardian, a free open-source system that converts bare-metal servers into the operational substrate for a one-person software company. Early days, still getting the infra set up.

The purpose is to create a free and open-source system for any being to convert a source of compute into a self-healing intelligent system (in our case, a secure, disaster-proof software company capable of generating revenue by providing value to the world).

The audience is a single individual with high technical ability who wants to build a company. Verself is the reference example — a value-providing, revenue-generating business proving the concept works — but it was hand-built (Nomad et al.); Guardian is the generalization, built so the next one isn't. The proof is autobiographical: the operator builds a successful company on Guardian first, then shows others the path.

The value proposition:

1. We make release and deployment automation easy.
2. We make supply chain, network, and application security easy.
3. We make it easy to add integrations (Stripe, GitHub, and the like) securely.
4. We make disaster recovery easy.
5. We make monitoring easy: the system detects its own degradation, remediates what it can, and pages the human only when it can't. Nothing else pages the human.

We do all of this by gluing together excellent existing tools and letting the user focus on building and iterating on their products. The economics: bootstrap once onto powerful fixed-cost metal, then iterate at near-zero marginal cost until product-market fit — ideas are fragile before they are refined, so shipping the next refined version must be nearly free. Every pillar is proven by a drill, not a claim: if it isn't drilled, it isn't true yet.

We use CozyStack. Grep through Cozystack 1.5 docs from the exact `v1.5.0` tag when validating 1.5.0 behavior, or the `release-1.5` branch when intentionally reading the maintained 1.5 line. Do not use v0 docs, v1.4 docs, or current main by accident.

Reference Cozystack for prior art for the cloud portion. Other inspiration: Zarf/UDS, AWS Landing Zone Accelerator, the airgapped landing zone pattern in general.

CozyStack is the platform; Hauler is the seed (the complete digest-pinned artifact bundle from which a Guardian is planted or revived, internet or not); provider profiles are the soil adapters (Latitude today). Guardian itself owns what no upstream can: the custody model, the proofs, and the bootstrap protocol. A provider must offer: boot of an arbitrary image, an isolated private segment with declared MTU, out-of-band reinstall/console, public IPv4, stable machine identity, and a reachable time source. That contract — not any provider's API — is the portability boundary.

Admission test for new components: anything added to Guardian must be (a) configuration of CozyStack, (b) content in the haul, (c) a value in a provider profile, or (d) custody, proof, or bootstrap protocol. If it is none of these, it does not get in.

<company_topology>
Single Global Writer
Public DNS stays globally managed, for now.

Cozystack tenants are coarse account boundaries, not the default unit of
application isolation. Start with one Guardian tenant under the required
Cozystack root/admin tenant. Use Kubernetes namespaces, labels, RBAC,
NetworkPolicy, service accounts, and release evidence for Guardian component
and stage isolation until a concrete operational need justifies another
Cozystack Tenant.

`tenant-root` is the required Cozystack root/admin tenant for a regional
management cluster. Cozystack packages/operators, Flux handoff, storage classes,
COSI/BackupClass/system bucket, root ingress/load-balancer substrate, root
infrastructure monitoring, child Tenant CRs, and cluster-wide policy belong
there. Do not put product workloads, product databases, or shared business logic
in `tenant-root`. tenant-root owns:

- Cozystack Package / operator declarations
- Flux source and Kustomizations that reconcile the management cluster
- the single `guardian` Tenant CR and any future account-boundary Tenant CRs
- storage substrate: StorageClasses, LINSTOR config, COSI classes
- backup substrate: BackupClass/cozy-default, system bucket plumbing
- root ingress/load-balancer substrate
- MetalLB / Cilium / Gateway substrate if cluster-scoped
- root DNS/bootstrap glue for cluster entrypoints: guardianintelligence.org
- root Monitoring for Kubernetes/Cozystack/storage/ingress health
- bootstrap/break-glass OpenBao only if needed for regional substrate recovery
- cluster-wide NetworkPolicy/RBAC/admission defaults
- cert-manager/issuer substrate if it serves all tenants
- External Secrets / SecretStore bootstrap needed for tenant secret projection
- operational drills for cluster survival: backup restore, node outage, OpenBao static-seal restart, system bucket validation

Today the active region is Latitude ASH (`ash`). The active management control
plane is the `guardian-mgmt` Kubernetes cluster. Its Kubernetes API endpoint is
the private VLAN VIP `https://10.8.0.250:6443`; public ingress still uses the
three Latitude node origins behind Cloudflare. Treat these files as the current
control-plane source of truth:

- `src/infrastructure/bootstrap/guardian-mgmt/main.tf`
- `src/infrastructure/talm/values.yaml`
- `src/infrastructure/base/cozystack/platform.yaml`
- `src/infrastructure/base/flux/sync.yaml`

```
region: ash
  cozystack-management-cluster: guardian-mgmt
    kubernetes-api: https://10.8.0.250:6443
    tenant-root                     # Cozystack substrate only

    tenant-guardian                 # Guardian-owned control planes/products
      # Guardian-owned root services such as OpenBao, release controllers, and
      # shared ops live directly in tenant-guardian unless they need a separate
      # account boundary.

      tenant-guardian-beta          # first durable integration stage
      tenant-guardian-gamma         # staging / release-candidate validation
      tenant-guardian-prod          # production

      labels:
        guardian.dev/component: iam | secrets | audit | telemetry | release | billing | aisucks | workloads | company
        guardian.dev/stage: beta | gamma | prod
        guardian.dev/tenant-id: gi-guardian

region: cmh                         # hypothetical future region
  cozystack-management-cluster: guardian-mgmt-cmh
    tenant-root
    tenant-guardian
```

Default release channels: Edge (CD on main), nightly, RC, stable.
Default deployment stages: beta, gamma, prod. Use `dev` only for local or
PR-preview workflows, not durable regional namespace names.
Release channels and deployment stages are not the same thing. Do not encode
release channels as Cozystack Tenants.
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
      secrets/
        openbao/                       # OpenBao substrate, policies, transit, projection
        api/                           # future Connect KMS/Secrets API when needed
        service/                       # future wrapper/control plane if needed
        release/
        deploy/base/

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
        openbao-kms/
        postgres-service/
        clickhouse-service/

      bootstrap/
        guardian-mgmt/                 # ASH bare-metal/OpenTofu root
        guardian-mgmt-dns/
        backend.tfvars

      talm/

      base/                            # reconciled into tenant-root
        kustomization.yaml
        cozystack/
        flux/
        networking/
        storage/
        backup/
        observability/
        registry/
        policy/
        tenants/                       # creates tenant-guardian by default

      deployments/
        guardian/                      # reconciled into tenant-guardian
          system/                      # OpenBao, release controllers, shared ops
          beta/
          gamma/
          prod/
        company/prod/                  # compatibility path until folded in

      tests/
      cmd/                             # infra validation/drill helpers
      load/
    tools/
</repo_shape>

<technology>
TLS terminates at Cloudflare edge.
Kubernetes API clients should target the `guardian-mgmt` private API VIP:
`https://10.8.0.250:6443`. Do not pin day-to-day kubeconfigs or drills to a
single control-plane node IP.
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

Technology inventories live in the repo, not in this file: `src/infrastructure/bootstrap/bundle/images.lock` is what runs (digest-pinned, conformance-tested); `src/tools/` is what we operate with (pinned CLIs: talm, talosctl, flux, kubectl, hauler, openbao, oras, k6); `MODULE.bazel` is what we build with.

Planned: Use Flagger for progressive delivery after Flux applies an approved digest. Use Kargo/Freight to promote immutable release candidates to service+stage+region targets.

Domain: guardianintelligence.org (abbreviated gi.org)

Objectives:

We're maximizing for safe operations (disaster-recovery from wiped box + offsite backups as priority 1, behind ongoing security checks + hardening) and highly continuous rapidly delivered software to external vendors like NPM/PyPi/Crates.io and so on.
Provisioning N workload nodes (rs4.metal.xlarge CPU: AMD 9554P, 64 Cores @ 3.1 GHz / RAM: 1.5 TB / Storage: 2 x 480 GB NVME + 4 x 8 TB NVME / NIC: 2 x 100 Gbps) is a first-class concept, but capacity is only added when a concrete workload pulls it — never provisioned ahead of expected demand.

Important context:
- All dependencies version/commit pinned. Nothing during runtime, dev time, test time, or build time should require external non-version-pinned tooling, or shell out to binaries outside this repo or its build artifacts.
- Never download unpinned versions of software or set an unpinned version as a dependency. Binaries are versioned, built, packaged, and installed by Bazel declarations.
- Container images are digest-pinned wherever this repo renders them. `src/infrastructure/bootstrap/bundle/images.lock` is the cold-bootstrap image inventory; the infra conformance test enforces that every image reference rendered from this repo is digest-pinned and present in the lock. Update the lock in the same PR as any image change.
- Cold-bootstrap trust model: the local checkout, its Bazel-built artifacts, and the operator custody bundle (static seal key + the operator env file) are everything a from-nothing bring-up may require. Bootstrap-only compromises are allowed, but the cluster must converge to the declared steady state afterward.
- Dev tools: `aspect`. Run `aspect tidy` to format the codebase.
- Don't use CUE. Avoid custom schemas, protocols, shell scripts, contracts. Lean towards production-ready implementations for CRDs and ensure Flux-operated Kubernetes can converge state without making CLI execution a second control plane.
- API IDL in Protobuf/Connect. Define IAM, audit, risk, request-size, rate limit, and idempotency metadata as explicit operation policy on the RPC contract.
- Protobuf governance uses the repo-pinned Buf toolchain through Bazel: linting, formatting, and breaking-change checks run from `rules_buf`; code generation uses local pinned generators only. Do not use Buf remote plugins in build/test/release paths.
- All operations must run unattended, no human-in-the-loop.
- Invent nothing. If we write our own code, it should be glue code over existing libraries and apeing reference implementations of solutions to problems only. Always do the boring industry-standard thing. Component choices are made by bake-off: candidates researched, losers rejected with recorded reasons, the winner pinned (the Hauler decision is the template). Months spent recreating an existing tool poorly is the cardinal failure mode.
- Code is not the truth for how the system works. Traces are.
- Use SQLC.
- Do not provide time estimates.

<development_loop>
- This section is WIP, follow best practices. The below is just a few things to add to normal development workflow
- Do not use CLI commands as a control plane. Rely on flux to converge the cluster on merged commits.
- Run `aspect infra edge-health` to smoke-test edge reachability post convergence. Verify DNS resolution for every configured `guardianintelligence.org` hostname, HTTPS behavior through the public edge and origin consistency checks.
- Edge failover drills are single-node exercises. Run the drill once per node by explicit node IP, wait for the node and public edge to recover, document that node's outage window, then move to the next node. A node whose loss breaches 60 seconds of public-edge disruption is load bearing and must be fixed before continuing.
- RTO policy lives in `docs/reliability-rto.md`.
</development_loop>

Constraints:
- Secrets must be autoprovisioned/autorotated.
- Guardian tenant OpenBao uses static auto-unseal plus OpenBao self-init. The
  static seal key is 32 raw bytes, placed out of band on each dedicated
  key-bearing node, and never stored in Kubernetes, Git, CI, chat, shell
  history, Talos machine files, or OpenBao-backed secret paths. Node/root
  compromise on a key-bearing node is OpenBao compromise. The runbook is
  `src/infrastructure/runbooks/openbao-static-seal-self-init.md`.
- Cozystack 1.5 backups use the platform-managed `cozy-default` BackupClass
  and system bucket. Do not add Guardian-specific backup strategies, backup
  credential Secrets, or checks for legacy backup object names.
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

Planned Product Surfaces:

- GitHub App (20x faster CI than GitHub Actions; adapted from the Verself repo; running untrusted customer CI requires TEE on the rs4 workload nodes first). (Not Yet Implemented)
- Software Company from an API call or web surface; host come-up tooling only prepares machines for the management cluster. (Not Yet Implemented)

Milestones:

Guardian advances only by drills passed and products shipped — never by components added. Substrate work must be pulled by a product need, a drill, or a milestone gate; it is never pushed because it would be elegant. Automate an operation on its second occurrence — the first time, do it by runbook and write the runbook down. Do not recreate the retired `guardian` CLI as a generic operator surface; day-to-day convergence belongs to OpenTofu, Talm, Cozystack, Flux, and standard Kubernetes controllers.

- M1 — The substrate is invincible. Drill #1 (all-node cold boot from Git + custody) has passed. Remaining: the wiped-node drill (including etcd-member and Node-object debris cleanup) and the dark cold-boot drill from the haul alone. Gate: revival with zero internet and zero undocumented steps.
- M2 — One product flows unattended. The company site through the full loop: merge → converge → canary → promote, synthetics watching all environments, alerts wired. Gate: a deliberately bad deploy detects itself and rolls back with hands off the keyboard; a yank drill passes. Flagger and Kargo earn admission here, pulled by this gate.
- M3 — Verself, by strangler. One service at a time onto Guardian; Nomad keeps running until each service proves parity. Stripe and GitHub integration patterns become reusable platform capability here, pulled by real need — never speculatively. Gate: a revenue-bearing Verself path served by Guardian for 30 days without regression.
- M4 — Guardian is downloadable. Canonical iPXE image + haul + CLI, with a single-box dev variant shipping the same way; most of the machinery falls out of M1's dark drill. Gate: a from-zero Guardian stood up on a second provider from the public artifact and docs alone. External adoption becomes a live option here, not before.
- M5 — Iteration is free. Many products, one fixed-cost fleet. Gate: shipping a new product idea requires no new infrastructure decisions and no new spend; the counterfactual invoice — what this month's actual workloads would have cost on managed cloud services — is computed from live metrics and published.
- M6 — The pair. Two Guardians (a second region, or one trusted peer) exchange encrypted backups. Gate: a cross-Guardian restore drill passes. The Guardian network grows from this seed if it should; the pair alone already solves offsite backup and the bus factor.

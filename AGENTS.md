This is a Bazel polyglot hermetically sealed monorepo for Guardian, a free open-source system that converts bare-metal servers into the operational substrate for a one-person software company. Early days, still getting the infra set up.

The purpose is to create a free and open-source system for any being to convert a source of compute into a self-healing intelligent system (in our case, a secure, disaster-proof software company capable of generating revenue by providing value to the world).

* Cozystack 1.5 `isp-full` - when researching CozyStack, use 1.5 docs from the exact `v1.5.0` tag / `release-1.5` branch. See `src/infrastructure/base/cozystack/platform.yaml` and `src/infrastructure/base/apps/core-services.yaml`
* Other useful reference architectures: Zarf/UDS, AWS Landing Zone Accelerator
* Repo ships specific products within the architecture. First major product: Verself (reference Blacksmith.sh)
* Airgapped hermetically-sealed come up done through images.lock + Rancher Hauler + Sidero Labs `talm` for Talos on bare metal soil (currently Latitude.sh)
* DNS managed through Cloudflare. TLS terminates at Cloudflare edge. Cloudflare LB for the three control plane nodes. [206.223.228.101, 45.250.254.119, 206.223.228.87].
* Cozystack tenancy - `guardian` tenant for product workloads, product databases, shared business logic, services, split per stage (gamma, beta, prod). `tenant-root` is the required Cozystack root/admin tenant for a regional management cluster. Cozystack packages/operators, Flux handoff, storage classes, COSI/BackupClass/system bucket, root ingress/load-balancer substrate, root infrastructure monitoring, child Tenant CRs, and cluster-wide policy go in `tenant-root`.
* Today the active region is Latitude ASH (`ash`). The active management control plane is the `guardian-mgmt` Kubernetes cluster. Its Kubernetes API endpoint is the private VLAN VIP `https://10.8.0.250:6443`. Reference files:
  - `src/infrastructure/bootstrap/guardian-mgmt/main.tf`
  - `src/infrastructure/talm/values.yaml`
  - `src/infrastructure/base/cozystack/platform.yaml`
  - `src/infrastructure/base/flux/sync.yaml`
* Stripe is payment rail only -- we don't use Stripe Subscriptions / Usage-Based Billing. We meter on our own (planned)
* Secrets via a single OpenBao instance for the whole cluster; stage isolation is at the policy layer, not the instance layer. Access is scoped per consumer Kubernetes namespace: `guardian-reader-<ns>` / `guardian-writer-<ns>` role pairs confined to `kv/guardian/guardian-mgmt/<namespace>/*`
* Zero customers as of present day besides us: no compatibility shims or legacy wrappers.
* OCI images are shipped to ghcr.io. See https://github.com/orgs/guardian-intelligence/packages
* Auth n/z is multitenant by default: Keycloak instance per stage. SpiceDB/Zanzibar for permissions. Currently just "Sign in With GitHub" supported. Future "Sign in With Guardian" with us as the OIDC provider and multiple connected accounts planned.
  - beta: https://beta.guardianintelligence.org/realms/verself/broker/github/endpoint
  - gamma: https://gamma.guardianintelligence.org/realms/verself/broker/github/endpoint
  - prod: https://guardianintelligence.org/realms/verself/broker/github/endpoint
* VictoriaLogs for logs. VictoriaMetrics for Metrics. TigerBeetle for financial truth and OLTP (planned). ClickHouse for analytics and Otel correlations/traces/spans. CNPG (single writer per stage, fan out read replicas) for system stage and misc.
* Bazel owns the build graph and produces bytes using OCI for layout. `cosign`/SLSA proves that it's authentic Guardian Intelligence LLC software.
* Runtime technology inventory: `src/infrastructure/bootstrap/bundle/images.lock` is what runs (digest-pinned, conformance-tested); `src/tools/` is what we operate with (pinned CLIs: talm, talosctl, flux, kubectl, hauler, openbao, oras, k6); `MODULE.bazel` is what we build with.
* Flagger used for blue/green deployments (Keycloak).
* Kargo for deployment promotions from beta -> gamma -> prod. GitHub app configured for auto-commits. Release channels for distributed binaries: Edge (CD on main), nightly, RC, stable.
* Domain: guardianintelligence.org (abbreviated in conversation with user as "gi.org")

<repo_shape>
The below is the target shape -- repo still changing and does not match this quite yet

src/
    products/
      viteplus-monorepo/               # vite-plus (vp) web workspace
        apps/
          guardianintelligence-web/    # gi.org company site; site/ holds the OCI push targets
        packages/
          brand/

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
    tools/                             # Non-runtime tooling (doggo for DNS etc.)
</repo_shape>

<technology>

Kubernetes API clients should target the `guardian-mgmt` private API VIP:
`https://10.8.0.250:6443`.

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
- Do not use CLI commands as a second control plane. Rely on flux to converge the cluster on merged commits.
- You can run `aspect infra edge-health` to smoke-test edge reachability post convergence. Verify DNS resolution for every configured `guardianintelligence.org` hostname, HTTPS behavior through the public edge and origin consistency checks.
- For drills (not part of normal development) run them once per node by explicit node IP, wait for the node and public edge to recover, document that node's outage window, then move to the next node. A node whose loss breaches 60 seconds of public-edge disruption is load bearing and must be fixed before continuing.
- RTO policy lives in `docs/reliability-rto.md`.
</development_loop>

Constraints:
- Secrets must be autoprovisioned/autorotated. To safely configure secrets per-environment, read `docs/secrets.md`.
- Cozystack 1.5 backups use the platform-managed `cozy-default` BackupClass  and system bucket. Do not add Guardian-specific backup strategies, backup credential Secrets, or checks.
- Traces are the only admissable proof -- ClickHouse (when stood up), Victoria Metrics, Victoria Logs. Collect traces/spans and relevant log lines to support your thesis that your task is complete to satisfaction. Test services under heavy load via k6 to surface subtle bugs.

Service architecture:

- Releasing distributed software is a one-way door: after a CLI, SDK, crate, wheel, or desktop/mobile artifact is public, rollback means publishing a new artifact and helping consumers move. Its gates must get stricter as it approaches stable.

Sandbox isolation doctrine (multi-tenant QEMU), the below is advisory for when we stand up our QEMU warm pool:

- Trust model: every sandbox guest is hostile and may hold a kernel exploit. The isolation boundary is KVM plus a jailed VMM, never namespaces alone — containers share a kernel and a kernel escape is a fleet escape. Untrusted-code planes (customer CI, agent sandboxes) run only on workload nodes, never on control-plane or product hosts.
- The bar: meet AWS Firecracker's software-isolation posture, minus hardware confidential computing. We do not yet have SEV-SNP/TDX encrypted memory or TPM-attested guests (SEV is off in BIOS on the f4 boxes; the rs4 plane is unverified), so memory-in-use confidentiality against a malicious host/operator is an accepted gap, deferred until the Confidential Computing Consortium path matures and we enable TEE on the rs4 workload nodes (Phase 4). Until then the host is trusted; tenants are isolated from each other, not from us. This is runtime memory confidentiality, distinct from build/release attestation (which needs no TEE).
- VMM substrate: QEMU/KVM with `-cpu host`; q35 only where a feature requires it (virtio-mem hotplug needs ACPI/PCI), otherwise prefer `-machine microvm`. Keep cloud-hypervisor (Rust, memory-safe, VFIO-capable) as the standing drop-in alternate VMM target — the jail and network model below are VMM-agnostic.
- Device-surface minimization: virtio-only (virtio-blk, virtio-net, virtio-serial/vsock). `-nodefaults`, `-no-user-config`, `-nographic`. No USB, floppy, CD, audio, or emulated legacy NIC/block. Patch QEMU device-emulation CVEs on the critical path — the standing tax of a C VMM.
- VMM process jail: each QEMU runs (1) as a dedicated unprivileged uid/gid, never root — the worker creates the TAP, then the process drops privilege; (2) under `-sandbox on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny` plus a supervisor seccomp-bpf whitelist with no_new_privs; (3) in its own user/mount/net/pid namespaces, pivot_root'd into a minimal rootfs holding only its drives and sockets; (4) under a per-VM AppArmor/SELinux (sVirt-style) label so an escaped QEMU cannot reach another tenant's disk; (5) in a cgroup v2 slice with memory.max/high, cpu.max, pids.max, io.max — PSI on the slice is the host saturation signal.
- Network isolation: one TAP + dedicated netns per VM, default-deny egress with explicit NAT, anti-spoof nftables on MAC+IP, per-VM bandwidth cap, no inter-VM L2 reachability, metadata reachable only at our controlled link-local endpoint. Ape OpenComputer's per-VM /30 but terminate it in a netns, not a shared bridge.
- Memory density without cross-tenant leakage: no cross-tenant KSM (write-timing side channel + Flip Feng Shui Rowhammer). Golden-image RAM savings come from a read-only shared base image (page-cache shared, never written) plus a per-sandbox COW overlay (ZFS clone) — only known-public content is ever shared. The density multiplier is hibernation/oversubscription of idle VMs, which shares nothing live.

Planned Product Surfaces:

- Verself - GitHub App (20x faster CI than GitHub Actions; adapted from the Verself repo; running untrusted customer CI requires TEE on the rs4 workload nodes first). (Not Yet Implemented)
- Empire - Software Company from an API call or web surface; host come-up tooling only prepares machines for the management cluster. (Not Yet Implemented)

Milestones:

Guardian advances only by drills passed and products shipped. Automate an operation on its second occurrence — the first time, do it by runbook and write the runbook down. Do not recreate the retired `guardian` CLI as a generic operator surface (yet, that's for the unscoped work on "Empire" ).

- M1 — The substrate is invincible. Drill #1 (all-node cold boot from Git + custody) has passed. Remaining: the wiped-node drill (including etcd-member and Node-object debris cleanup) and the dark cold-boot drill from the haul alone. Gate: revival with zero internet and zero undocumented steps. (complete)
- M2 — One product flows unattended. The company site through the full loop: merge → converge → canary → promote, synthetics watching all environments, alerts wired. Gate: a deliberately bad deploy detects itself and rolls back with hands off the keyboard; a yank drill passes. Flagger and Kargo earn admission here, pulled by this gate. (complete)
- M3 — Verself, by strangler. One service at a time onto Guardian; Nomad keeps running until each service proves parity. Stripe and GitHub integration patterns become reusable platform capability here, pulled by real need — never speculatively. Gate: a revenue-bearing Verself path served by Guardian for 30 days without regression.
- M4 — Guardian is downloadable. Canonical iPXE image + haul + CLI, with a single-box dev variant shipping the same way; most of the machinery falls out of M1's dark drill. Gate: a from-zero Guardian stood up on a second provider from the public artifact and docs alone. External adoption becomes a live option here, not before.
- M5 — Iteration is free. Many products, one fixed-cost fleet. Gate: shipping a new product idea requires no new infrastructure decisions and no new spend; the counterfactual invoice — what this month's actual workloads would have cost on managed cloud services — is computed from live metrics and published.
- M6 — The pair. Two Guardians (a second region, or one trusted peer) exchange encrypted backups. Gate: a cross-Guardian restore drill passes. The Guardian network grows from this seed if it should; the pair alone already solves offsite backup and the bus factor.

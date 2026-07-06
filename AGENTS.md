This is a Bazel polyglot hermetically sealed monorepo for Guardian, a free open-source system that converts bare-metal servers into the operational substrate for a one-person software company. Early days, still getting the infra set up.

The purpose is to create a free and open-source system for any being to convert a source of compute into a self-healing intelligent system (in our case, a secure, disaster-proof software company capable of generating revenue by providing value to the world) as a platform to build sophisticated software products such as Verself, a GitHub App that speeds up your CI.

* Cozystack 1.5 `isp-full` - when researching CozyStack, use 1.5 docs from the exact `v1.5.2` tag / `release-1.5` branch. See `src/infrastructure/base/cozystack/platform.yaml` and `src/infrastructure/base/apps/core-services.yaml`
* Other useful reference architectures: Zarf/UDS, AWS Landing Zone Accelerator
* Repo ships specific products within the architecture. First major product: Verself (reference Blacksmith.sh)
* Airgapped hermetically-sealed come up done through images.lock + Rancher Hauler + Sidero Labs `talm` for Talos on bare metal soil (currently Latitude.sh)
* DNS managed through Cloudflare. TLS terminates at Cloudflare edge. Cloudflare LB for the three control plane nodes. [206.223.228.101, 45.250.254.119, 206.223.228.87].
* Cozystack tenancy IS the stage-isolation mechanism: stages are child Tenants of `tenant-guardian`, declared in `src/infrastructure/deployments/guardian/system/stage-tenants.yaml` (beta/gamma/prod/previews, each with `spec.host: <stage>.guardianintelligence.org`). Staged product workloads deploy into the derived namespaces `tenant-guardian-<stage>` (reference: `src/infrastructure/deployments/iam/<stage>/`). Cozystack's generated Cilium policies give sibling default-deny between stages for free; the hand-written per-stage `CiliumClusterwideNetworkPolicy` pairs in `deployments/iam/*/networkpolicy.yaml` compensate for a Cozystack v1.5.x depth-2 ancestor-label bug that blocks the root ingress from reaching grandchild tenants (see `docs/research/cozystack-1.5/`), and become partially droppable after the upstream fix ships. Never model stages as per-application tenants: tenant names ban dashes, app×stage nesting hits the depth bug, siblings have no first-class peering.
* Kargo is decoupled from tenancy: namespaces like `guardian-iam` and `company-site` are Kargo *project* namespaces holding only promotion CRs and secrets plumbing — not workloads. Stage promotion steps edit Git paths (`deployments/<vertical>/<stage>/…`), never workload namespaces, so tenancy changes cannot break promotions. Reference wiring: `src/infrastructure/deployments/guardian/promotion/pipelines/`. Cross-stage system services (analytics, alerting, verself-runner, OpenBao in `tenant-guardian`) deliberately live outside stage tenants because they serve all stages.
* Stage tenants are static and long-lived; never delete/recreate one and never create tenant-per-PR (previews are Deployments inside the one static `previews` tenant). Cozystack 1.5.x tenant deletion runs an unpinned `bitnami/kubectl:latest` pre-delete hook that is absent from `images.lock` — guaranteed-broken in dark mode — and upstream has deletion wedge modes; static tenants sidestep all of it.
* `tenant-root` is the required Cozystack root/admin tenant for a regional management cluster. Cozystack packages/operators, Flux handoff, storage classes, BackupClass, root ingress/load-balancer substrate, root infrastructure monitoring, child Tenant CRs, and cluster-wide policy go in `tenant-root`.
* Single region right now (`ash` Ashburn, Virginia Latitude region). The active management control plane is the `guardian-mgmt` Kubernetes cluster. Its Kubernetes API endpoint is the private VLAN VIP `https://10.8.0.250:6443`. Reference files:
  - `src/infrastructure/bootstrap/guardian-mgmt/main.tf`
  - `src/infrastructure/talm/values.yaml`
  - `src/infrastructure/base/cozystack/platform.yaml`
  - `src/infrastructure/base/flux/sync.yaml`
* Stripe is payment rail only -- we don't use Stripe Subscriptions / Usage-Based Billing. We meter on our own (planned)
* Secrets via a single OpenBao instance for the whole cluster; stage isolation is at the policy layer, not the instance layer. Access is scoped per consumer Kubernetes namespace: `guardian-reader-<ns>` / `guardian-writer-<ns>` role pairs confined to `kv/guardian/guardian-mgmt/<namespace>/*`
* Zero customers as of present day besides us: no compatibility shims or legacy wrappers.
* OCI images are shipped to ghcr.io. See https://github.com/orgs/guardian-intelligence/packages
* Auth n/z is multitenant by default: Keycloak instance per stage (product IAM, running in `tenant-guardian-<stage>`). Distinct from Cozystack's bundled *platform* Keycloak (operator identity for dashboard/kubectl OIDC), which is disabled — see `src/infrastructure/base/cozystack/platform.yaml` for the rationale. SpiceDB/Zanzibar for permissions. Currently just "Sign in With GitHub" supported. Future "Sign in With Guardian" with us as the OIDC provider and multiple connected accounts planned.
  - beta: https://beta.guardianintelligence.org/realms/verself/broker/github/endpoint
  - gamma: https://gamma.guardianintelligence.org/realms/verself/broker/github/endpoint
  - prod: https://guardianintelligence.org/realms/verself/broker/github/endpoint
- API IDL in Buf/Connect + (AIP-193). Declare each operation's policy surface (e.g. required permission, idempotency key, request-size, rate-limit class, audit level) outside of the core event contract as method-options metadata on the RPC contract. We need to be able to fine tune operational characterstics that don't break the schema. See `src/proto/guardian`. `connect.Interceptor`s enforce it fails-closed.
* VictoriaLogs for logs. VictoriaMetrics for Metrics. TigerBeetle for financial truth and OLTP (planned). ClickHouse for analytics and Otel correlations/traces/spans. CNPG (single writer per stage, fan out read replicas) for system stage and misc.
* Bazel owns the build graph and produces bytes using OCI for layout. `cosign`/SLSA proves that it's authentic Guardian Intelligence LLC software.
* Runtime technology inventory: `src/infrastructure/bootstrap/bundle/images.lock` is what runs (digest-pinned, conformance-tested); `src/tools/` is what we operate with (pinned CLIs: talm, talosctl, flux, kubectl, hauler, openbao, oras, k6); `MODULE.bazel` is what we build with.
* Flagger used for blue/green deployments for Keycloak (see `src/infrastructure/deployments/iam/`). Canary releases for non-tier-1 service components.
* Kargo for deployment promotions from beta -> gamma -> prod. GitHub app configured for auto-commits. Release channels for distributed binaries: Edge (CD on main), nightly, RC, stable.
* Domain: guardianintelligence.org (abbreviated in conversation with user as "gi.org")
* Object Storage is handled by R2, including backups. Fully bare metal topology on NVME so it makes no economical sense to reserve expensive fast drives for object storage. No SeaweedFS.
* `guardian-mgmt` private API VIP: `https://10.8.0.250:6443`. MetalLB for L2/ARP inside the Latitude VLAN. `10.8.0.200 - 10.8.0.240`. Public edge is `Service.type=LoadBalancer` backed by MetalLB/Cilium allocation and announcement, with Cloudflare Load Balancing in front for WAF, TLS, health checks, and failover. Cloudflare origins are the three Latitude public node IPs, and the public edge must stay stateless so Cloudflare can steer around unhealthy origins per request.
* Never download unpinned versions of software or set an unpinned version as a dependency. Binaries are versioned, built, packaged, and installed by Bazel declarations.
* Container images are digest-pinned wherever this repo renders them. `src/infrastructure/bootstrap/bundle/images.lock` is the cold-bootstrap image inventory; the infra conformance test enforces that every image reference rendered from this repo is digest-pinned and present in the lock. Update the lock in the same PR as any image change.
* Cold-bootstrap trust model: the local checkout, its Bazel-built artifacts, and the operator custody bundle (static seal key + the operator env file) are everything a from-nothing bring-up may require. Bootstrap-only compromises are allowed, but the cluster must converge to the declared steady state afterward.
* Dev tools: `aspect`. Run `aspect tidy` to format the codebase.
* Don't use CUE. Avoid custom schemas, protocols, shell scripts, contracts. Lean towards production-ready implementations for CRDs and ensure Flux-operated Kubernetes can converge state without making CLI execution a second control plane.
* Protobuf governance uses the repo-pinned Buf toolchain through Bazel: linting, formatting, and breaking-change checks run from `rules_buf`; code generation uses local pinned generators only. Do not use Buf remote plugins in build/test/release paths.
* All operations must run unattended, no human-in-the-loop.
* Invent nothing. If we write our own code, it should be glue code over existing libraries and apeing reference implementations of solutions to problems only. Always do the boring industry-standard thing. Component choices are made by bake-off: candidates researched, losers rejected with recorded reasons, the winner pinned (the Hauler decision is the template). Months spent recreating an existing tool poorly is the cardinal failure mode.
* Code is not the truth for how the system works. Traces are.
* Use SQLC for Go service PG queries.
* To safely configure secrets per-environment, read `docs/secrets.md`.
* You are not alone in this repo. Expect parallel changes by the user or other agents and work around them to avoid destructive action.
* No need to be precious with git hygiene. If you see a doc update, it's fine to fold it into your worktree or branch, even if it's unrelated.
* For every feature we ship, we must assume that if we don't have a canary actively asserting it works, that it's broken. If the user suggests a feature or large project, work backwards from the monitoring and operations story: how can we be notified when the feature breaks, or when performance or availability drops, and how do we avoid shipping regressions in the first place using promotion gates and responsible deployment practices? We have the technology necessary to do so, we just have to remember to use them.
* This cluster is k8s v1.34.3 (VAP is GA)

<observability>
- Logs: `kubectl port-forward -n tenant-root svc/vlselect-generic 9471:9471`, then LogsQL via `curl 127.0.0.1:9471/select/logsql/query --data-urlencode 'query=...'`.
- Metrics: `kubectl port-forward -n tenant-root svc/vmselect-shortterm 8481:8481`, then PromQL via `curl 127.0.0.1:8481/select/0/prometheus/api/v1/query --data-urlencode 'query=...'`.
- Traces, spans, and analytics events: `kubectl port-forward -n guardian-analytics svc/clickhouse-analytics 9000:9000`, get the `ingest` password from `kubectl get secret -n guardian-analytics analytics-clickhouse-users -o jsonpath='{.data.ingest}' | base64 -d`, then `clickhouse-client --host 127.0.0.1 --user ingest` and `SHOW CREATE TABLE guardian_analytics.events` / `guardian_analytics.otel_traces` for the schema actually being served.
- Schema source: `src/infrastructure/analytics/events-table.sql` (canonical events DDL) and `src/infrastructure/deployments/analytics/system/{ddl-configmap.yaml,traces-configmap.yaml}` (what's actually rendered and applied in-cluster).
</observability>

<repo_shape>
The below is the target shape -- repo still changing and does not match this quite yet
.aspect                                # Aspect tasks
src/
    products/
      viteplus-monorepo/               # vite-plus (vp) web workspace
        apps/
          guardianintelligence-web/    # gi.org company site; site/ holds the OCI push targets
        packages/
          brand/

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
        guardian/
          system/                      # OpenBao, stage Tenant CRs, shared ops (→ tenant-guardian)
          promotion/                   # Kargo controller + pipelines (project namespaces)
          flagger/
        iam/{beta,gamma,prod}/         # per-stage Keycloak (→ tenant-guardian-<stage>)
        company/{prod,previews}/       # company site (→ tenant-guardian-prod / -previews)
        analytics/system/              # cross-stage (→ guardian-analytics)
        alerting/                      # cross-stage (→ tenant-root)
        verself-runner/                # cross-stage (→ verself-runner)

      tests/
      cmd/                             # infra validation/drill helpers
      load/
    tools/                             # Non-runtime tooling (doggo for DNS etc.)
</repo_shape>

<overall_strategy>

The audience for cloners of this repo is a single individual or a small team with high technical ability that want to transform an idea into a serious software company. Verself is the reference example — a value-providing, revenue-generating business proving the concept works — but it was hand-built (Nomad et al.); Guardian is the generalization, built so the next one isn't. The proof is autobiographical: the user (Shovon Hasan/"anveio") builds a successful company on Guardian's core infrastructure first, proving the core platform works and can be used to rapidly build any kind of product.

The value proposition:

1. We make release and deployment automation easy.
2. We make supply chain, network, and application security easy.
3. We make it easy to add integrations (Stripe, GitHub, and the like) securely.
4. We make disaster recovery easy.
5. We make monitoring easy: the system detects its own degradation, remediates what it can, and pages the human only when it can't. Nothing else pages the human.

We do all of this by gluing together excellent existing tools and letting the user focus on building and iterating on their products. The economics: bootstrap once onto powerful fixed-cost metal, then iterate at near-zero marginal cost until product-market fit — ideas are fragile before they are refined, so shipping the next refined version must be nearly free.
We're currently maximizing for highly continuous rapidly delivered software to external vendors like NPM/PyPi/Crates.io and so on.
</overall_strategy>

<development_loop>
- This section is WIP, follow best practices. The below is just a few things to add to normal development workflow
- Do not use CLI commands as a second control plane. Rely on flux to converge the cluster on merged commits.
- You can run `aspect infra edge-health` to smoke-test edge reachability post convergence. Verify DNS resolution for every configured `guardianintelligence.org` hostname, HTTPS behavior through the public edge and origin consistency checks.
- For drills (not part of normal development) run them once per node by explicit node IP, wait for the node and public edge to recover, document that node's outage window, then move to the next node. A node whose loss breaches 60 seconds of public-edge disruption is load bearing and must be fixed before continuing.
- RTO policy lives in `docs/reliability-rto.md`.
</development_loop>

Constraints:
- Database backups target off-cluster Cloudflare R2 through Cozystack's platform backup machinery (path pending; no in-cluster object storage exists to back up to). Do not add Guardian-specific backup strategies, backup credential Secrets, or checks.
- Traces are the only admissable proof -- ClickHouse (when stood up), Victoria Metrics, Victoria Logs. Collect traces/spans and relevant log lines to support your thesis that your task is complete to satisfaction. Test services under heavy load via k6 to surface subtle bugs.

Service architecture:

- Releasing distributed software is a one-way door: after a CLI, SDK, crate, wheel, or desktop/mobile artifact is public, rollback means publishing a new artifact and helping consumers move. Its gates must get stricter as it approaches stable.



Planned Product Surfaces:

- Verself - GitHub App (20x faster CI than GitHub Actions; adapted from the Verself repo; running untrusted customer CI requires TEE on the rs4 workload nodes first). (Not Yet Implemented)
- Empire - Software Company from an API call or web surface; host come-up tooling only prepares machines for the management cluster. (Not Yet Implemented)

Milestones:

Guardian advances only by drills passed and products shipped. Automate an operation on its second occurrence — the first time, do it by runbook and write the runbook down. Do not recreate the retired `guardian` CLI as a generic operator surface (yet, that's for the unscoped work on "Empire" ).

- M1 — The substrate is invincible. Drill #1 (all-node cold boot from Git + custody) has passed. Remaining: the wiped-node drill (including etcd-member and Node-object debris cleanup) and the dark cold-boot drill from the haul alone. Gate: revival with zero internet and zero undocumented steps. (complete)
- M2 — One product flows unattended. The company site through the full loop: merge → converge → canary → promote, synthetics watching all environments, alerts wired. Gate: a deliberately bad deploy detects itself and rolls back with hands off the keyboard; a yank drill passes. Flagger and Kargo earn admission here, pulled by this gate. (complete)
- M3 — Verself ported over. Stripe and GitHub integration patterns become reusable platform capability here. Gate: a revenue-bearing Verself path served by Guardian for 30 days without regression.
- M4 — Guardian is downloadable. Canonical iPXE image + haul + CLI, with a single-box dev variant shipping the same way; most of the machinery falls out of M1's dark drill. Gate: a from-zero Guardian stood up on a second provider from the public artifact and docs alone. External adoption becomes a live option here, not before.

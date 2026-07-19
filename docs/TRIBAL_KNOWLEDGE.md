# Tribal knowledge

## Cloudflare bootstrap credentials

Guardian uses three separate Cloudflare credentials for the ASH management
cluster edge. Keep the runtime controller token narrow; keep provisioner and
state credentials outside the cluster so a wiped cluster can still be rebuilt.

| Credential | Consumer | Durable home | Scope |
| - | - | - | - |
| `cloudflare_external_dns_api_token` | ExternalDNS in `external-dns` | OpenBao path `kv/guardian/guardian-mgmt/tenant-guardian/dns/external-dns`, projected by External Secrets Operator | Zone `guardianintelligence.org`: `Zone Read`, `DNS Read`, `DNS Write` |
| `cloudflare_dns_lb_provisioner_api_token` | OpenTofu root `src/infrastructure/bootstrap/guardian-mgmt-dns` during edge bootstrap or recovery | Off-cluster break-glass or CI secret store; injected only into the apply environment | Account: `Load Balancing: Monitors and Pools Read`, `Load Balancing: Monitors and Pools Write`; Zone `guardianintelligence.org`: `Zone Read`, `DNS Read`, `DNS Write`, `Load Balancers Read`, `Load Balancers Write` |
| `cloudflare_r2_access_key_id` and `cloudflare_r2_secret_access_key` | OpenTofu S3-compatible backend for repo-owned state | Off-cluster break-glass or CI secret store; injected only into the apply environment | R2 `Object Read & Write`, scoped to the OpenTofu state bucket `guardian-vault` |

User must pay $10/mo to enable CloudFlare LB with 3 endpoints (1 for each ingress node). This is not enabled by default.

## Talos access from the operator workstation

- The live talosconfig is `src/infrastructure/talm/talosconfig` (gitignored;
  its encrypted twin `talosconfig.encrypted` is committed — decryption is
  covered by the cold-boot runbook). **Do not trust `~/.talos/config`**: it
  holds endpoints of a previous cluster generation and every one of them
  times out. If `talosctl` hangs on port 50000, you are almost certainly
  using the stale global config.
- Current node public IPs are recorded in the `# talm:` modeline on the
  first line of each `src/infrastructure/talm/nodes/*.yaml` — that is the
  source of truth and it changes on reimage. Port 50000 is open on those
  IPs from the operator workstation.
- The kube API is reachable via the default `~/.kube/config`, whose only
  standing identity is the `platform-agent` OIDC context (set up with
  `aspect infra auth --platform-agent`). There is no standing admin
  kubeconfig anywhere on disk; breakglass x509 is minted on demand with
  `aspect infra auth --platform-admin --reason "<why>"` and dies with its
  short cert lifetime.
- Machine config applies are per-node, base plus overlay:
  `talm apply -f nodes/<node>.yaml -f nodes/<node>-overlay.yaml`.

## Regenerating node configs (`talm template -I`)

The install-disk regression is fixed (`talos.install.disk_pin` emits
`diskSelector.serial`; a bare `/dev/nvmeXn1` can point at a different
physical disk on the next boot). Regen output is still not byte-convergent:
talm's re-marshal drops quotes and reorders map keys, discovered-disk
comments follow boot enumeration order, and live network state (hostname,
MTU, VLANConfig) echoes into the base files that the `*-overlay.yaml` files
own. Review regen diffs hunk-by-hunk before committing them; never commit a
`diskSelector` → `disk:` change.

## Hardware watchdog (armed on all nodes since PR #338)

Every node arms its AMD SP5100 TCO chipset watchdog (`/dev/watchdog0`,
1m timeout) via a `WatchdogTimerConfig` document; a hard kernel hang
reboots the node with no operator action (measured: 2m22s crash → Ready,
OpenBao replica auto-unsealed at +4m). Verify with
`talosctl get watchdogtimerstatuses` (sysfs `state` must read `active`).
To re-run the positive test: set `kernel.panic=0` first (Talos defaults it
to 10, and a panic self-reboot contaminates the result), then
`echo c > /proc/sysrq-trigger` from a privileged debug pod; afterwards
`/sys/class/watchdog/watchdog0/bootstatus` must read 32 (WDIOF_CARDRESET —
the chipset recording that it caused the last reset). Watchdog recoveries
are silent by design; the dead-man's-switch alerting work is what makes
them observable.

## Promotion enforcement lives partly in repo settings (not Git)

The Kargo promotion bot is untrusted by construction only while GitHub
enforces the checks: branch protection on `main` requiring the `build`,
`site-gate`, `analytics-gate`, and `derive` contexts (`derive` is the
images-lock-sign job that derives the union images lock from the full
checkout — digest pinning, declared/rendered disjointness, dark-mirror host
coverage), plus repo-level allow-auto-merge. Those settings are not
represented in Git — re-assert them if the repo is ever recreated or
protection is accidentally dropped:

```sh
gh api -X PATCH repos/guardian-intelligence/guardian \
  -F allow_auto_merge=true
gh api -X PUT repos/guardian-intelligence/guardian/branches/main/protection \
  --input - <<'JSON'
{
  "required_status_checks": {"strict": false, "contexts": ["build", "site-gate", "analytics-gate", "derive"]},
  "enforce_admins": false,
  "required_pull_request_reviews": null,
  "restrictions": null
}
JSON
```

All required checks run on every PR (`site-gate` and `derive` classify the
diff themselves and exit fast when nothing relevant changed — required
checks cannot be path-filtered without hanging unrelated PRs). The `guardian-promotions`
GitHub App (private key in operator custody; also in the repo Actions
secrets for promotion-automerge) must stay scoped to Contents + Pull
requests read/write.

## Watching the cluster converge (`aspect infra watch`)

When you push a change, watch whether it actually reconciles instead of
guessing. Two read-only views:

```sh
aspect infra watch                       # live Flux status with repo-pinned kubectl
aspect infra watch --mode=convergence    # ntfy stream: Flux convergence alerts only
aspect infra watch --mode=stream         # ntfy stream: all alerts, no cluster access
```

Use the default live status view to babysit a PR: it reads Kustomization and HelmRelease
Ready conditions every few seconds and prints exactly the ones that are not
Ready, with the reason and message (`BuildFailed`, `HealthCheckFailed`,
`UpgradeFailed`, ...). A bad manifest shows up in seconds. The ntfy stream is
pager-oriented: many rules intentionally hold for ~15m before firing, so it
tells you about *sustained* failure, not a fresh push.

The stream view needs nothing but network: the cluster already publishes
every alert to the `guardian-operations-fable` ntfy topic (via
`alert-relay`), and that topic is world-subscribable, so from any machine:

```sh
curl -s "https://ntfy.sh/guardian-operations-fable/json?since=15m"
```

is the zero-dependency equivalent of the stream view. Override the topic
with `NTFY_TOPIC` / `NTFY_BASE` if the sink ever moves. (If the topic is
later locked down, subscribers will need a token — the relay is the single
place that knows the URL today.)

<scratchpad>
* Cluster autorotates CA every 90 days
* The three management nodes boot factory Sidero-signed Talos UKIs with UEFI
  Secure Boot enabled. Talos encrypts STATE, EPHEMERAL, and the LINSTOR raw
  volume with TPM-backed LUKS2; customer and business PVCs add Cozystack-native
  LINSTOR LUKS. The control and audit evidence are in
  `docs/management-cluster-trusted-boot-and-storage.md`.
* Automated etcd snapshots to R2
</scratchpad>

* Cozystack 1.5.2 `isp-full` - when researching CozyStack, use 1.5 docs from the exact [`release-1.5`](https://github.com/cozystack/cozystack/tree/v1.5.2) tag. See `src/infrastructure/base/cozystack/platform.yaml` and `src/infrastructure/base/apps/core-services.yaml`
* Other useful reference architectures: Zarf/UDS, AWS Landing Zone Accelerator
* Repo ships specific products within the architecture. First major product: Postflight (reference Blacksmith.sh)
* Airgapped hermetically-sealed come up done through the generated union images lock (declared + rendered, `//src/infrastructure/cmd/imageset`) + Rancher Hauler + Sidero Labs `talm` for Talos on bare metal soil (currently Latitude.sh).
* DNS managed through Cloudflare. TLS terminates at Cloudflare edge for proxied workload/HTTP hostnames, not the DNS-only `k8s.guardianintelligence.org` control-plane API name. Cloudflare LB for the three control plane nodes. [206.223.228.101, 45.250.254.119, 206.223.228.87].
* Cloudflare config has exactly five owners; drift between declared and live edge config is a defect. Workloads own only their origin HTTP contract (Cache-Control/ETag headers — Electric is the reference) and never hold edge credentials. The in-cluster external-dns controller owns workload DNS records, reconciled from Git-declared CRs with a DNS-only token. Traffic substrate (load balancers, monitors, pools) and the `k8s.guardianintelligence.org` control-plane A records are declared in `src/infrastructure/bootstrap/guardian-mgmt-dns/` — a minimal DR actor whose empty plan is the cold-boot drift oracle. Zone edge policy (AOP, cache rulesets, bot management, zone settings) is declared in `src/infrastructure/bootstrap/guardian-mgmt-edge-policy/`, whose token cannot move traffic. The API tokens themselves are owned by `src/infrastructure/bootstrap/guardian-mgmt-cloudflare-tokens/`: every lane token is minted there as an account-owned token from the single custody-carried minter (Account API Tokens Write — root-equivalent, never in-cluster), so lane roots read their credential from that root's output and custody carries one Cloudflare secret instead of one per lane. Nothing is edited in the dashboard: a dashboard change is either backported into its root or reverted by the next apply.
* `tenant-root` is the required Cozystack root/admin tenant for a regional management cluster. Cozystack packages/operators, Flux handoff, storage classes, BackupClass, root ingress/load-balancer substrate, root infrastructure monitoring, child Tenant CRs, and cluster-wide policy go in `tenant-root`.
* Cozystack tenancy is the stage-isolation mechanism: stages are child Tenants of `tenant-guardian`, declared in `src/infrastructure/deployments/guardian/system/stage-tenants.yaml` (prod/previews, each with `spec.host: <stage>.guardianintelligence.org`). We test in prod behind feature flags — prod is the only promotion stage; previews hold ephemeral per-PR site deployments. Product workloads deploy into the derived namespaces `tenant-guardian-<stage>` (reference: `src/infrastructure/deployments/iam/prod/`). Cozystack's generated Cilium policies give sibling default-deny between tenants for free; the hand-written per-stage `CiliumClusterwideNetworkPolicy` pairs in `deployments/iam/*/networkpolicy.yaml` compensate for a Cozystack v1.5.x depth-2 ancestor-label bug that blocks the root ingress from reaching grandchild tenants (see `docs/adrs/0004-stages-are-cozystack-tenants.md`), and become partially droppable after the upstream fix ships. Never model stages as per-application tenants: tenant names ban dashes, app×stage nesting hits the depth bug, siblings have no first-class peering.
* Kargo is decoupled from tenancy: namespaces like `guardian-iam` and `company-site` are Kargo *project* namespaces holding only promotion CRs and secrets plumbing — not workloads. Stage promotion steps edit Git paths (`deployments/<vertical>/<stage>/…`), never workload namespaces, so tenancy changes cannot break promotions. Reference wiring: `src/infrastructure/deployments/guardian/promotion/pipelines/`. Cross-stage system services (analytics, alerting, postflight-runner, OpenBao in `tenant-guardian`) deliberately live outside stage tenants because they serve all stages.
* Stage tenants are static and long-lived; never delete/recreate one and never create tenant-per-PR (previews are Deployments inside the one static `previews` tenant).
* Single region right now (`ash` Ashburn, Virginia Latitude region). The active management control plane is the `guardian-mgmt` Kubernetes cluster. Its Kubernetes API endpoint is the private VLAN VIP `https://10.8.0.250:6443`. Reference files:
  - `src/infrastructure/bootstrap/guardian-mgmt/main.tf`
  - `src/infrastructure/talm/values.yaml`
  - `src/infrastructure/base/cozystack/platform.yaml`
  - `src/infrastructure/base/flux/sync.yaml`
* Stripe is payment rail only -- we don't use Stripe Subscriptions / Usage-Based Billing. We meter on our own (planned)
* Secrets via a single OpenBao instance for the whole cluster; stage isolation is at the policy layer, not the instance layer. Access is scoped per consumer Kubernetes namespace: `guardian-reader-<ns>` / `guardian-writer-<ns>` role pairs confined to `kv/guardian/guardian-mgmt/<namespace>/*`
* Zero customers as of present day besides us: no compatibility shims or legacy wrappers.
* OCI images are shipped to ghcr.io. See https://github.com/orgs/guardian-intelligence/packages
* Customer identity runs in the product Keycloak in `tenant-guardian-prod`, distinct from Cozystack's bundled *platform* Keycloak (operator identity for dashboard/kubectl OIDC), which gates cluster-admin access: kubectl authenticates via `aspect infra auth --platform-agent` (OIDC against the `cozy` realm); the custody x509 kubeconfig is breakglass-only, minted by `aspect infra auth --platform-admin --reason "<why>"` (audit-logged), and the Keycloak admin console is never publicly routed — see `src/infrastructure/base/cozystack/platform.yaml` and `keycloak-admin-guard.yaml` there. The customer issuer is `https://guardianintelligence.org/realms/guardianintelligence.org`; upstream social identities are connections to its stable Guardian accounts. SpiceDB is reached only through the typed Authorization API for organization, project, repository, installation, and role decisions, and is not on the login path. The complete invariants and canary contract are in `docs/sign-in-with-guardian.md`.
- API IDL in Buf/Connect + (AIP-193). Declare each operation's policy surface (e.g. required permission, idempotency key, request-size, rate-limit class, audit level) outside of the core event contract as method-options metadata on the RPC contract. We need to be able to fine tune operational characterstics that don't break the schema. See `src/proto/guardian`. `connect.Interceptor`s enforce it fails-closed.
* VictoriaLogs for logs. VictoriaMetrics for Metrics. TigerBeetle is the selected financial system of record: one three-replica production cluster fixed to `ash-earth`, `ash-wind`, and `ash-water`, with one encrypted local data file per node and a one-node failure budget. It is not synthetic-only; customer admission is gated by `docs/tigerbeetle.md`, and the accepted topology is ADR 0011. ClickHouse is product infrastructure, not platform infrastructure: the analytics and timeseries DB for the products (Postflight, website), one consolidated instance (`analytics`) — never per-signal or per-purpose instances; load tests run as scratch tables on the real instance and are dropped afterwards. CNPG (single writer per stage, fan out read replicas) for system stage and misc.
* Bazel owns the build graph and produces bytes using OCI for layout. `cosign`/SLSA proves that it's authentic Guardian Intelligence LLC software.
* Runtime technology inventory: what runs is the union of the digest-pinned image refs rendered from the manifest trees and `src/infrastructure/bootstrap/bundle/images.declared.lock` (what runs WITHOUT being rendered: bootstrap artifacts, system images, operator-spawned workloads, Go-tool-referenced job images) — the union lock is generated, never edited; `src/tools/` is what we operate with (pinned CLIs: talm, talosctl, flux, kubectl, hauler, openbao, oras, k6); `MODULE.bazel` is what we build with.
* Flagger used for blue/green deployments for non-platform Keycloak (see `src/infrastructure/deployments/iam/`). Canary releases for non-tier-1 service components.
* Kargo for deployment promotions straight to prod (first-party surfaces auto-promote; mission-critical third-party images — Keycloak, Electric — promote deliberately). GitHub app configured for auto-commits. Release channels for distributed binaries: Edge (CD on main), nightly, RC, stable.
* Domain: guardianintelligence.org (abbreviated in conversation with user as "gi.org")
* Object Storage is handled by R2, including backups. Fully bare metal topology on NVME so it makes no economical sense to reserve expensive fast drives for object storage. No SeaweedFS.
* `guardian-mgmt` private API VIP: `https://10.8.0.250:6443`. MetalLB for L2/ARP inside the Latitude VLAN. `10.8.0.200 - 10.8.0.240`. Public edge is `Service.type=LoadBalancer` backed by MetalLB/Cilium allocation and announcement, with Cloudflare Load Balancing in front for WAF, TLS, health checks, and failover. Cloudflare origins are the three Latitude public node IPs, and the public edge must stay stateless so Cloudflare can steer around unhealthy origins per request.
* Never download unpinned versions of software or set an unpinned version as a dependency. Binaries are versioned, built, packaged, and installed by Bazel declarations.
* Container images are digest-pinned wherever this repo renders them, with the registry named explicitly (never `grafana/k6`-style default-registry refs). The cold-bootstrap inventory is the GENERATED union of those rendered refs and `images.declared.lock`; the infra conformance tests enforce digest pinning, declared/rendered disjointness, and dark-mirror host coverage. A rendered image change needs no lock edit; only images nothing renders (operator-spawned, bootstrap artifacts) are declared by hand.
* Cold-bootstrap trust model: the local checkout, its Bazel-built artifacts, the operator vault (Cloudflare account login, custody passphrase, age identity), and the R2-replicated custody artifacts (custody bundle, age-encrypted bootstrap set, OpenBao raft snapshots) are everything a from-nothing bring-up may require — see `docs/secrets.md`. Bootstrap-only compromises are allowed, but the cluster must converge to the declared steady state afterward.
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
* This cluster is k8s v1.36.2 (VAP is GA)
* Drills are not part of normal development — run them when asked on one node at a time by explicit node IP, wait for the node and public edge to recover, document that node's outage window, then move to the next. A node whose loss breaches 60 seconds of public-edge disruption is load-bearing and must be fixed before continuing.
* Development tools are version-pinned in `src/tools/` (talm, talosctl, flux, kubectl, hauler, openbao, oras, k6); use those and run the install, never an ambient install.
* RTO policy lives in `docs/reliability-rto.md`.
* Run load tests, add them as a Kargo-linked gate if they're missing and include load testing as part of planning. Load tests are the best way to measure the durability and performance of our system. Act accordingly on the data.


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
        iam/prod/                      # product Keycloak (→ tenant-guardian-prod)
        company/{prod,previews}/       # company site (→ tenant-guardian-prod / -previews)
        analytics/system/              # cross-stage (→ guardian-analytics)
        alerting/                      # cross-stage (→ tenant-root)
        postflight-runner/                # cross-stage (→ postflight-runner)

      tests/
      cmd/                             # infra validation/drill helpers
      load/
    tools/                             # Non-runtime tooling (doggo for DNS etc.)
</repo_shape>


<overall_strategy>

The audience for cloners of this repo is a single individual or a small team with high technical ability that want to transform an idea into a serious software company. Postflight is the reference example — a value-providing, revenue-generating business proving the concept works — but it was hand-built: the [`verself` repo](https://github.com/guardian-intelligence/verself) is Postflight's reference implementation, running on Nomad + Firecracker on a single node, not our current tech stack. Guardian is the generalization, built so the next one isn't. The proof is autobiographical: the user (Shovon Hasan/"anveio") builds a successful company on Guardian's core infrastructure first, proving the core platform works and can be used to rapidly build any kind of product.

The value proposition:

1. We make release and deployment automation easy.
2. We make supply chain, network, and application security easy.
3. We make it easy to add integrations (Stripe, GitHub, and the like) securely.
4. We make disaster recovery easy.
5. We make monitoring easy: the system detects its own degradation, remediates what it can, and pages the human only when it can't. Nothing else pages the human.

We do all of this by gluing together excellent existing tools and letting the user focus on building and iterating on their products. The economics: bootstrap once onto powerful fixed-cost metal, then iterate at near-zero marginal cost until product-market fit — ideas are fragile before they are refined, so shipping the next refined version must be nearly free.
We're currently maximizing for highly continuous rapidly delivered software to external vendors like NPM/PyPi/Crates.io and so on.
</overall_strategy>

Milestones:

Guardian advances only by drills passed and products shipped. Automate an operation on its second occurrence — the first time, do it by runbook and write the runbook down. Do not recreate the retired `guardian` CLI as a generic operator surface (yet, that's for the unscoped work on "Empire" ).

- M1 — The substrate is invincible. Drill #1 (all-node cold boot from Git + custody) has passed. Remaining: the wiped-node drill (including etcd-member and Node-object debris cleanup) and the dark cold-boot drill from the haul alone. Gate: revival with zero internet and zero undocumented steps. (complete)
- M2 — One product flows unattended. The company site through the full loop: merge → converge → canary → promote, synthetics watching all environments, alerts wired. Gate: a deliberately bad deploy detects itself and rolls back with hands off the keyboard; a yank drill passes. Flagger and Kargo earn admission here, pulled by this gate. (complete)
- M3 — Postflight ported over. Stripe and GitHub integration patterns become reusable platform capability here. Gate: a revenue-bearing Postflight path served by Guardian for 30 days without regression.

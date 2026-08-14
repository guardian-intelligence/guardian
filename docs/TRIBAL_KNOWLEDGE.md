# Tribal knowledge

Hard-won facts that are not obvious from the code. Most of this file is
cluster-substrate lore; most development is product work that needs almost
none of it. Use the routing below and read only what your task touches.

## Route by task

- **Wake Up, Mythra! (WUM)** — most development happens here. Read
  `docs/wake-up-mythra-development.md` (plan of record and architecture
  invariants), `docs/wum-local-dev.md` (one-command local loop), and
  `docs/netcode.md` (server-authoritative simulation contract). Gating client
  behavior: `docs/feature-flags.md`. Shipping a user-visible feature:
  `docs/canaries.md` and `docs/loadtest.md`. Following a user session across
  the stack: `docs/trace-correlation.md`. The substrate sections below matter
  only when deploying or operating mythrad in the cluster.
- **Postflight** — `docs/postflight-architecture.md` is the entry point and
  names its companions
  (`postflight-{product,fleet,host,scheduling,storage,security-model,runner-lifecycle,lightning,cli-distribution}.md`).
- **Company site, letters, PrivateCut** — `docs/letters-cms.md` (letters live
  in Directus, not Git), `docs/trace-correlation.md`, `docs/canaries.md`.
- **Identity and sign-in** — `docs/sign-in-with-guardian.md`;
  `docs/github-apps.md` for the first-party App inventory.
- **Payments and billing** — `docs/payments.md`, `docs/tigerbeetle.md`,
  `docs/tigerbeetle-financial-model.md`.
- **Secrets** — `docs/secrets.md`; it routes to `docs/openbao-design.md` and
  `docs/openbao-residue-inventory.md`.
- **Dependencies and supply chain** — `docs/dependency-management.md`,
  `docs/supply-chain-design.md`, `docs/registry-design.md`.
- **Cluster access from an agent environment** —
  `docs/agent-environment-authentication.md` (routes to the hosted-agent
  variants).
- **Cluster and infrastructure work** — the substrate sections below, plus
  `docs/admission-doctrine.md`, `docs/tofu-gitops-design.md`,
  `docs/management-cluster-trusted-boot-and-storage.md`, and
  `docs/reliability-rto.md`.

## Rules for every change

* Never download unpinned versions of software or set an unpinned version as a dependency. Binaries are versioned, built, packaged, and installed by Bazel declarations. This includes tools in src/tools.
* Invent nothing. If we write our own code, it should be glue code over existing libraries and apeing reference implementations of solutions to problems only. Prefer the boring industry-standard thing. Component choices are made by bake-off: candidates researched, losers rejected with recorded reasons, the winner pinned (the Hauler decision is the template). Months spent recreating an existing tool poorly is the cardinal failure mode.
* Avoid custom schemas, protocols, shell scripts, contracts.
* Zero customers as of present day besides us: no compatibility shims or legacy wrappers.
* For every feature we ship, we must assume that if we don't have a canary actively asserting it works, that it's broken. If the user suggests a feature or large project, work backwards from the monitoring and operations story: how can we be notified when the feature breaks, or when performance or availability drops, and how do we avoid shipping regressions in the first place using promotion gates and responsible deployment practices? We have the technology necessary to do so, we just have to remember to use them. Canary principles live in `docs/canaries.md`.
* Run load tests. Load tests are the best way to measure the durability and performance of our system. We must understand the maximum throughput of our system: individual components and blackbox user-session-simulations.
* The goal is to make operations run unattended, no human-in-the-loop.
* To safely configure secrets per-environment, read `docs/secrets.md`. Adding, rotating, or wiring a secret never opens the custody bundle: it is a Git PR plus one `bao kv put` through a namespace-scoped writer token. Custody is sealed for disaster recovery, cold boot, and CA/seal-key rotation, and `TestCustodyCeremonyConfinedToRecoveryRunbooks` fails the build if any other document teaches the restore ceremony. The bootstrap OpenTofu roots reconcile in-cluster (`docs/tofu-gitops-design.md`), so a routine root change is a PR, not a custody open; the workstation `tofu apply` survives only as break-glass.
* Do not use GitHub Actions workflow YAMLs as a second control plane. Prefer to move tasks including but not limited to: generating Preview Deployments, generating/signing images, scheduled jobs, and so on, into the source code, rather than hairpinning cluster administration through GitHub.
* You are not alone in this repo. Expect parallel changes by the user or other agents and work around them to avoid destructive action.
* No need to be precious with git hygiene. If you see a doc update, it's fine to fold it into your worktree or branch, even if it's unrelated.

### Service and API conventions

* API IDL in Buf/Connect + (AIP-193). Declare each operation's policy surface (e.g. required permission, idempotency key, request-size, rate-limit class, audit level) outside of the core event contract as method-options metadata on the RPC contract. We need to be able to fine tune operational characterstics that don't break the schema. See `src/proto/guardian`. `connect.Interceptor`s enforce it fails-closed.
* Protobuf governance uses the repo-pinned Buf toolchain through Bazel: linting, formatting, and breaking-change checks run from `rules_buf`; code generation uses local pinned generators only. Do not use Buf remote plugins in build/test/release paths.
* Use SQLC+pgx for Go service PG queries.

## Company context

* Domain: guardianintelligence.org (abbreviated in conversation with user as "gi.org")
* Repo ships specific products within the architecture. First major product: Postflight (reference Blacksmith.sh and verself)
* Stripe is payment rail only -- we don't use Stripe Subscriptions / Usage-Based Billing. We meter on our own (planned)
* Other useful reference architectures: Zarf/UDS, AWS Landing Zone Accelerator

<overall_strategy>
We are an open source reference architecture in addition to the Postflight core product. The value proposition for cloners:

1. We make release and deployment automation easy.
2. We make supply chain, network, and application security easy.
3. We make it easy to add integrations (Stripe, GitHub, and the like) securely.
4. We make disaster recovery easy.
5. We make monitoring easy: the system detects its own degradation, remediates what it can, and pages the human only when it can't. Nothing else pages the human.

We do all of this by gluing together excellent existing tools and letting the user focus on building and iterating on whatever their particular product is. The economics: bootstrap once onto powerful fixed-cost metal, then iterate at near-zero marginal cost until product-market fit — ideas are fragile before they are refined, so shipping the next refined version must be nearly free.
</overall_strategy>

## Cluster and platform architecture

* Cozystack 1.6.0 `isp-full` - when researching Cozystack, use the exact [`v1.6.0`](https://github.com/cozystack/cozystack/tree/v1.6.0) tag. See `src/infrastructure/base/cozystack/platform.yaml` and `src/infrastructure/base/apps/core-services.yaml`
* This cluster is k8s v1.36.2 (VAP is GA)
* Single region right now (`ash` Ashburn, Virginia Latitude region). The active management control plane is the `guardian-mgmt` Kubernetes cluster. Its Kubernetes API endpoint is the private VLAN VIP `https://10.8.0.250:6443`. Reference files:
  - `src/infrastructure/bootstrap/guardian-mgmt/main.tf`
  - `src/infrastructure/talm/values.yaml`
  - `src/infrastructure/base/cozystack/platform.yaml`
  - `src/infrastructure/base/flux/sync.yaml`
* `tenant-root` is the required Cozystack root/admin tenant for a regional management cluster. Cozystack packages/operators, Flux handoff, storage classes, BackupClass, root ingress/load-balancer substrate, root infrastructure monitoring, child Tenant CRs, and cluster-wide policy go in `tenant-root`.
* Cozystack tenancy is the stage-isolation mechanism: stages are child Tenants of `tenant-guardian`, declared in `src/infrastructure/deployments/guardian/system/stage-tenants.yaml`. Prod owns the platform apex `guardianintelligence.org`, while previews owns `previews.guardianintelligence.org`. We test in prod behind feature flags — prod is the only promotion stage; previews carries no workloads and exists as the empty namespace the admission canaries probe. Product workloads deploy into the derived namespaces `tenant-guardian-<stage>` (reference: `src/infrastructure/deployments/iam/prod/`). Cozystack's generated Cilium policies give sibling default-deny between tenants for free; the hand-written per-stage `CiliumClusterwideNetworkPolicy` pairs in `deployments/iam/*/networkpolicy.yaml` admit the host-networked root ingress by its `host` and `remote-node` identities without opening sibling traffic. Never model stages as per-application tenants: tenant names ban dashes, app×stage nesting consumes the namespace-length budget, and siblings have no first-class peering.
* Stage tenants are static and long-lived; never delete/recreate one and never create a tenant per PR or per application.
* Kargo is decoupled from tenancy: namespaces like `guardian-iam` and `company-site` are Kargo *project* namespaces holding only promotion CRs and secrets plumbing — not workloads. Stage promotion steps edit Git paths (`deployments/<vertical>/<stage>/…`), never workload namespaces, so tenancy changes cannot break promotions. Reference wiring: `src/infrastructure/deployments/guardian/promotion/pipelines/`. Cross-stage system services (analytics, alerting, postflight-runner, OpenBao in `tenant-guardian`) deliberately live outside stage tenants because they serve all stages.
* Kargo for deployment promotions straight to prod (first-party surfaces auto-promote; mission-critical third-party images — Keycloak — promote deliberately). GitHub app configured for auto-commits. Release channels for distributed binaries: Edge (CD on main), nightly, RC, stable.
* Flagger used for blue/green deployments for non-platform Keycloak (see `src/infrastructure/deployments/iam/`). Canary releases for non-tier-1 service components.
* DNS managed through Cloudflare. TLS terminates at Cloudflare edge for proxied workload/HTTP hostnames, not the DNS-only `k8s.guardianintelligence.org` control-plane API name. Cloudflare LB for the three control plane nodes. [206.223.228.101, 45.250.254.119, 206.223.228.87].
* Cloudflare config has exactly five owners; drift between declared and live edge config is a defect. Workloads own only their origin HTTP contract (Cache-Control/ETag headers) and never hold edge credentials. The in-cluster external-dns controller owns workload DNS records, reconciled from Git-declared CRs with a DNS-only token. Traffic substrate (load balancers, monitors, pools) and the `k8s.guardianintelligence.org` control-plane A records are declared in `src/infrastructure/bootstrap/guardian-mgmt-dns/` — a minimal DR actor whose empty plan is the cold-boot drift oracle. Zone edge policy (AOP, cache rulesets, bot management, zone settings) is declared in `src/infrastructure/bootstrap/guardian-mgmt-edge-policy/`, whose token cannot move traffic. The API tokens themselves are owned by `src/infrastructure/bootstrap/guardian-mgmt-cloudflare-tokens/`: every lane token is minted there as an account-owned token from the single custody-carried minter (Account API Tokens Write — root-equivalent, never in-cluster), so lane roots read their credential from that root's output and custody carries one Cloudflare secret instead of one per lane. Nothing is edited in the dashboard: a dashboard change is either backported into its root or reverted by the next apply.
* `guardian-mgmt` private API VIP: `https://10.8.0.250:6443`. MetalLB for L2/ARP inside the Latitude VLAN. `10.8.0.200 - 10.8.0.240`. Public edge is `Service.type=LoadBalancer` backed by MetalLB/Cilium allocation and announcement, with Cloudflare Load Balancing in front for WAF, TLS, health checks, and failover. Cloudflare origins are the three Latitude public node IPs, and the public edge must stay stateless so Cloudflare can steer around unhealthy origins per request.
* Object Storage is handled by R2, including backups. Fully bare metal topology on NVME so it makes no economical sense to reserve expensive fast drives for object storage. No SeaweedFS.
* VictoriaLogs for logs. VictoriaMetrics for Metrics. TigerBeetle for financial truth/OLTP.
* Customer identity runs in the product Keycloak in `tenant-guardian-prod`, distinct from Cozystack's bundled *platform* Keycloak (operator identity for dashboard/kubectl OIDC), which gates cluster-admin access: kubectl authenticates via `aspect infra auth` (OIDC against the `cozy` realm, default `read` persona); the custody x509 kubeconfig is breakglass-only, minted by `aspect infra auth --persona=root --reason "<why>"` (audit-logged), and the Keycloak admin console is never publicly routed — see `src/infrastructure/base/cozystack/platform.yaml` and `src/infrastructure/base/cozystack-identities/keycloak-admin-guard.yaml`. The customer issuer is `https://guardianintelligence.org/realms/guardianintelligence.org`; upstream social identities are connections to its stable Guardian accounts. SpiceDB is reached only through the typed Authorization API for organization, project, repository, installation, and role decisions, and is not on the login path. The complete invariants and canary contract are in `docs/sign-in-with-guardian.md`.
* RTO policy lives in `docs/reliability-rto.md`.

### Build, images, and supply chain

* Bazel owns the build graph and produces bytes using OCI for layout. `cosign`/SLSA proves that it's authentic Guardian Intelligence LLC software.
* OCI images are shipped to ghcr.io. See https://github.com/orgs/guardian-intelligence/packages
* Container images are digest-pinned wherever this repo renders them, with the registry named explicitly (never `grafana/k6`-style default-registry refs). The cold-bootstrap inventory is the GENERATED union of those rendered refs and `images.declared.lock`; the infra conformance tests enforce digest pinning, declared/rendered disjointness, and dark-mirror host coverage. A rendered image change needs no lock edit; only images nothing renders (operator-spawned, bootstrap artifacts) are declared by hand.
* Runtime technology inventory: what runs is the union of the digest-pinned image refs rendered from the manifest trees and `src/infrastructure/bootstrap/bundle/images.declared.lock` (what runs WITHOUT being rendered: bootstrap artifacts, system images, operator-spawned workloads, Go-tool-referenced job images) — the union lock is generated, never edited; `src/tools/` is what we operate with (pinned CLIs: talm, talosctl, flux, kubectl, hauler, openbao, oras, k6); `MODULE.bazel` is what we build with.

## Operator workstation and Talos

### Talos access from the operator workstation

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
- The apiserver firewall admits only the operations VPS (`operatorSubnets`
  in `src/infrastructure/talm/values.yaml`). Any other workstation (e.g. a
  macOS dev machine) runs `tools/ops/mgmt-tunnel install` once: launchd then
  keeps an SSH tunnel on `127.0.0.1:16443` through the VPS, and both
  `aspect infra auth` and kubectl use it automatically — guardian_auth probes
  the loopback port before the direct endpoint, so no flags are needed on
  either kind of machine.
- The kube API is reachable via the default `~/.kube/config`, whose only
  standing identity is the `read` persona (the `platform-agent` OIDC context,
  set up with `aspect infra auth`): cluster-wide read plus port-forward, and the
  only rung that refreshes unattended. Product-specific operations derive
  short-lived capability identities (`aspect mythra ...` is the first); repair
  verbs outside those narrow roles come from
  `--persona=write-basic` (non-root secret writes only) and tenant-root secret
  writes or emergencies from `--persona=write-all`; neither write persona
  holds `offline_access`, so each costs an operator device approval and expires
  with its Keycloak session. There is no standing admin kubeconfig anywhere on
  disk; breakglass x509 is minted on demand with
  `aspect infra auth --persona=root --reason "<why>"` and dies with its short
  cert lifetime. The ladder lives in
  `src/infrastructure/base/cozystack-identities/platform-admins.yaml`.
- Workstation hygiene is a launchd agent, not a habit: `tools/ops/workspace-watch
  install` fast-forwards the primary checkout whenever that is a no-op for local
  work, removes worktrees whose branch is already in `origin/main`, and keeps the
  `read` persona's offline token from idling out of its 30-day window. Locked,
  dirty, in-use, and recently touched worktrees are never removed —
  `git worktree lock` is the opt-out other agents get for free.
- Machine config applies are per-node, base plus overlay:
  `talm apply -f nodes/<node>.yaml -f nodes/<node>-overlay.yaml`.

### Regenerating node configs (`talm template -I`)

The install-disk regression is fixed (`talos.install.disk_pin` emits
`diskSelector.serial`; a bare `/dev/nvmeXn1` can point at a different
physical disk on the next boot). Regen output is still not byte-convergent:
talm's re-marshal drops quotes and reorders map keys, discovered-disk
comments follow boot enumeration order, and live network state (hostname,
MTU, VLANConfig) echoes into the base files that the `*-overlay.yaml` files
own. Review regen diffs hunk-by-hunk before committing them; never commit a
`diskSelector` → `disk:` change.

### Hardware watchdog (armed on all nodes since PR #338)

Every node arms its AMD SP5100 TCO chipset watchdog (`/dev/watchdog0`, 1m timeout) via a `WatchdogTimerConfig` document

<scratchpad>
* Cluster autorotates CA every 90 days
* The three management nodes boot factory Sidero-signed Talos UKIs with UEFI
  Secure Boot enabled. Talos encrypts STATE, EPHEMERAL, and the LINSTOR raw
  volume with TPM-backed LUKS2; customer and business PVCs add Cozystack-native
  LINSTOR LUKS. The control and audit evidence are in
  `docs/management-cluster-trusted-boot-and-storage.md`.
* Automated etcd snapshots to R2
</scratchpad>

### Drills

* Drills are not part of normal development — run them when asked on one node at a time by explicit node IP, wait for the node and public edge to recover, document that node's outage window, then move to the next. A node whose loss breaches 60 seconds of public-edge disruption is load-bearing and must be fixed before continuing.

## Bootstrap and disaster recovery

* Cloudflare bootstrap credentials: user must pay $10/mo to enable CloudFlare LB with 3 endpoints (1 for each ingress node). This is not enabled by default.
* Airgapped hermetically-sealed bootstrap done through the generated union images lock (declared + rendered, `//src/infrastructure/cmd/imageset`) + Rancher Hauler + Sidero Labs `talm` for Talos on bare metal soil (currently Latitude.sh).
* Cold-bootstrap trust model: the local checkout, its Bazel-built artifacts, the operator vault (Cloudflare account login, custody passphrase, age identity), and the R2-replicated custody artifacts (custody bundle, age-encrypted bootstrap set, OpenBao raft snapshots) are everything a from-nothing bring-up may require — see `docs/secrets.md`. Bootstrap-only compromises are allowed, but the cluster must converge to the declared steady state afterward.

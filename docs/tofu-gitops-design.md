# OpenTofu through GitOps

Six OpenTofu roots describe real infrastructure — the Latitude metal, every
Cloudflare token, zone edge policy, DNS and load balancing, main's ruleset and
the customer fleet, the Stripe sandbox. All six reconcile in-cluster from Git:
a per-root Kubernetes CronJob runs a first-party runner that fetches the same
source artifact Flux reconciles, runs tofu, and applies changes on a merge to
main. Operating the runner is `../src/infrastructure/runbooks/tofu-runner-operations.md`;
this document is the design.

It exists so three properties hold:

- **Git is the source of truth, not Git plus an operator's memory.** A merged
  change to `bootstrap/guardian-github/` reconciles on the root's next tick,
  the same way every other change to this repository takes effect. No CLI is a
  second control plane.
- **Drift is detected.** A root re-plans on its schedule; a plan that finds
  changes it will not apply is surfaced, not lost until a human happens to run
  one. #1132 was the worked example of the old failure — main's ruleset
  drifted, nothing diffed it, and it surfaced as unmergeable branches instead
  of an alert.
- **Custody stays sealed.** Reconciling in-cluster keeps the state-encryption
  passphrase and provider tokens out of daily circulation; the custody bundle
  opens only for disaster recovery and key rotation, as `secrets.md` intends.

Complements `secrets.md` (the tier model and why custody is sealed) and
`../src/infrastructure/runbooks/github-as-code.md` (the root that motivated
moving GitHub into tofu).

## Architecture

Each root is one `CronJob` in the `tofu-system` namespace running the
first-party **tofu-runner** (`../src/infrastructure/cmd/tofu_runner`). A tick:

1. reads the Flux source object (`GitRepository` in steady state,
   `OCIRepository` in the dark overlay) to learn the current artifact URL and
   digest — the runner's only apiserver access, a read-only `get` scoped by a
   `cozy-fluxcd` Role;
2. fetches that artifact from source-controller, verifies the digest, and
   extracts it — the same bytes every Flux `Kustomization` reconciles, so no
   git credential and no divergence from what Flux sees;
3. runs `tofu init`, then `tofu plan -detailed-exitcode`, and branches on the
   result.

There is no controller and no long-lived process: the CronJob schedule is the
reconcile interval, each tick is a fresh Job, and R2 conditional-write locking
(`use_lockfile`) is the mutex that serializes a scheduled Job against a
break-glass workstation apply.

**The mode ladder.** A root runs in one of two postures, set by the `MODE`
env on its CronJob (default `plan`):

| Posture | On a clean plan | On a plan with changes |
| --- | --- | --- |
| `plan` (the soak) | exit 0 | log the plan, exit 0 — hold |
| `apply` | exit 0 | `tofu apply` the saved plan |

A root is born in `plan` mode and soaks until its plans are clean and stable;
the flip to `apply` is its own reviewed PR that adds `MODE: apply`, in the
blast-radius order below. The plan text is the Job's pod log — the `read`
persona can tail it, so plans never land in a ConfigMap the view role can
read.

**The approval artifact is the merge to main.** An `apply`-mode root applies
when a PR lands, gated by the ruleset like every other change — reviewable,
attributable, revertable. A merge applies on the next scheduled tick;
`kubectl create job --from=cronjob/tofu-<root>` runs it immediately when that
wait is too long (the runbook has the recipe).

**Signalling is by exit code, not an Alerta credential the pod would have to
hold.** A tofu error, a failed apply, or drift on a page-on-drift root fails
the Job; the `tofu-runner-health` VMRule pages on a terminal Job failure not
yet superseded by a success, staleness, and KSM going blind — the same
posture as the etcd-snapshot CronJob. Drift on a `plan`-mode root exits 0 on
purpose (the soak) and is read from the log.

**The runner image** is first-party and slim: the pinned OpenTofu binary, a
CA bundle, and a packed provider filesystem mirror on a distroless base, no
shell. Its tofu is pinned to the same version as the multitool tofu the
break-glass path runs (`TestTofuRunnerTracksMultitoolPin`), so an in-cluster
apply and a hand apply plan identically. `tofu init` is hermetic: the baked
CLI config resolves providers only from the mirror — no `direct` fallback —
so a tick downloads nothing, a registry or GitHub-releases outage cannot
fail provider installation (plan and apply still need the root's provider
API), and a provider missing from the mirror fails init loudly.
The mirror ships the registry release zips pinned in `MODULE.bazel`; each
pin's sha256 must appear among the consuming roots' `.terraform.lock.hcl`
`zh:` hashes (`TestTofuRunnerProviderMirrorTracksRootLockfiles`), so the
image can only ship bytes the lockfiles already trust, and a Renovate
provider bump goes red until the lockfile and mirror move together. Flux
image automation moves the digest like any other first-party workload.

## The six roots

Every root reconciles in-cluster, flipped to `apply` in blast-radius order,
each with the guards that make auto-apply safe for it:

- `guardian-stripe-sandbox` — sandbox blast radius; the first mover.
- `guardian-mgmt-edge-policy` — its lane token writes zone policy only (no
  DNS-record or LB permission), so the worst compromise cannot move traffic.
- `guardian-github` — **split.** The root once carried a classic
  `repo`+`admin:org` PAT for exactly one resource:
  `github_app_installation_repository.promotions_homebrew_tap`, the write
  GitHub accepts from no App token and no fine-grained PAT. That binding is an
  owner-UI-managed fact recorded in `github-apps.md`, the same class as App
  installations themselves. Everything else — main's ruleset, the tap
  repository, the customer fleet — is managed in-cluster with **two per-org
  fine-grained PATs** (fine-grained PATs are scoped to a single resource
  owner): repository Administration on `guardian-intelligence`, repository
  create/administer on `digital-guardian-software`. No org-admin credential
  exists in-cluster; cluster compromise does not reach org administration.
- `guardian-mgmt-dns` — auto is sound because the reconcile loop (Flux to
  github.com, the runner to api.cloudflare.com) traverses nothing this root
  manages, so a bad apply cannot sever its own revert path: the worst case is
  a minutes-long ops-access window while a git revert converges. Zones are
  data sources, not resources. Guards: `lifecycle.prevent_destroy` on the
  k8s-API records, the LB pool and LBs, and CAA; `check` blocks converted to
  preconditions (checks are warn-only in OpenTofu); the pause procedure in the
  operations runbook; no automerge for Cloudflare provider bumps touching this
  root.
- `guardian-mgmt-cloudflare-tokens` — the minter root, **in-cluster by the
  informed override of the never-in-cluster doctrine, ruled 2026-08-08** with
  the cost stated: a runner compromise can mint Cloudflare tokens, including
  R2-write credentials. Why it is acceptable: the minter token is revocable in
  one console action from the account login, which stays in the operator vault
  (tier 1) — the cluster holds a lever, not the root of trust; every mint is a
  state change a drift plan surfaces; offline custody pulls mean even an
  R2-wipe mint cannot destroy the recovery path. Guards: the runner namespace
  is CNP-walled to the Cloudflare API; this root re-plans on the shortest
  schedule of any root (15 minutes) and carries `PAGE_ON_DRIFT`, so an
  out-of-band mint fails the Job and pages rather than soaking silently; the
  root's expiry `check` is a precondition so a plan cannot pass a dead token
  forward.
- `guardian-mgmt` — the metal. Last, because it is the substrate everything
  else runs on. Auto is bounded by `lifecycle.prevent_destroy` on every server
  resource — a destroy plan fails the apply and alerts — and metal changes are
  rare, deliberate PRs. Cold boot is unaffected: with no cluster there are no
  CronJobs, and the disaster path applies this root from a workstation per
  `cold-boot-bootstrap.md` with credentials from the operator vault.

## Credentials and state

**Runtime: OpenBao, the documented path.** The `tofu-system` namespace has the
standard `guardian-reader`/`guardian-writer` pair and a `ClusterSecretStore`;
each root's CronJob mounts ExternalSecret-materialized Secrets as `envFrom` —
the root's provider token(s), the R2 backend keypair, and the state
passphrase. All of it is reissuable third-party material, exactly what
`secrets.md` classes as OpenBao content.

**State encryption.** Every root's R2 state is encrypted under one pbkdf2 key
provider named `state`. The passphrase is **dual-homed**: OpenBao for the
runners, the operator vault for cold boot — ciphertext outlives every token,
so the one value that cannot be re-issued from a console is the one kept in
two places. The cluster never reads the vault, so dual-homing crosses no
boundary; it is the same arrangement the custody repository passphrase has.
(A key provider's *name* is embedded in the ciphertext metadata — see the
[[tofu-state-encryption]] note; renaming one without a same-ceremony
re-encrypt orphans the state.)

**Locking.** `use_lockfile` on R2 gives every root conditional-write locking,
which is what lets a scheduled Job and a break-glass workstation apply coexist
without a coordinator.

**Disaster recovery uses the operator vault, not a new tier.** Every input
other than the state passphrase (Latitude token, Cloudflare minter, R2
keypair) is re-issuable from its console at DR time — the accepted worst case
`secrets.md` already states. The custody bundle carries none of the tofu
values; it opens for disaster recovery and key rotation only.

## Break-glass

A root can be applied from a workstation when the CronJobs cannot run it (a
runner regression, or the cluster degraded but the provider reachable). It is
the only sanctioned workstation apply, it does not open the custody bundle,
and it runs the same tofu the image ships. The full recipe — suspend the
CronJob, assemble credentials from consoles and the operator vault, plan/apply
through the multitool tofu pin — is in
`../src/infrastructure/runbooks/tofu-runner-operations.md`.

## Risks

- **The runner holds every root's token, including the Cloudflare minter.**
  Bounded by per-root token scoping (the GitHub PATs cannot reach Cloudflare;
  edge-policy cannot move traffic), a non-cluster-admin ServiceAccount whose
  only apiserver grant is a read of one Flux source object, a CNP-walled
  namespace, plan text confined to pod logs, and console-revocable credentials
  whose root of trust (the account logins) never leaves the operator vault.
- **Auto-apply on a bad plan.** With no human between merge and apply,
  `prevent_destroy` on the irreplaceable resources (metal, k8s-API records,
  LBs, CAA) is the backstop: a destructive plan fails the apply and alerts
  instead of executing.
- **Drift versus incident-time manual changes.** A manual Cloudflare change
  made during an incident would be reverted by the next reconcile of an
  `apply`-mode root. The pause procedure (suspend the CronJob) exists for
  exactly this — and when the cluster is down enough to force manual DNS work,
  the CronJobs are down too, so the change sticks until the cluster returns.
- **A hostile or accidental mint.** The minter root's drift plan is the
  detector (page-on-drift, shortest schedule); console revocation from the
  vault-held account login is the response; offline custody pulls bound the
  damage of an R2-wipe mint.

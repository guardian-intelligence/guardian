# OpenTofu through GitOps: retiring the workstation apply

Status: ruled 2026-08-08 — all six roots reconcile in-cluster; build in
progress. Supersedes the 2026-07-26 two-tier ruling (recorded below under
"The tiering that was ruled out").

Complements `secrets.md` (the tier model and why custody is sealed) and
`../src/infrastructure/runbooks/github-as-code.md` (the root that motivated
this).

## The problem

Six OpenTofu roots describe real infrastructure — the Latitude metal, every
Cloudflare token, zone edge policy, DNS and load balancing, main's ruleset
and the customer fleet, the Stripe sandbox. All six are applied by a human
running `tofu apply` on a workstation. That breaks three things at once.

**It is a second control plane.** The house rule is that administration CLIs
are for reads and Flux converges the cluster after merge. A merged change to
`bootstrap/guardian-github/` does nothing until someone remembers to apply
it, so Git is not the source of truth — Git plus an operator's memory is.

**It keeps custody in daily circulation.** Every command that touches real
state needs `tofu_state_encryption_passphrase`, and each root additionally
needs its provider token and the R2 backend keypair. All of them live in
`custody.env`. A bundle whose declared steady state is *sealed*, whose every
open is supposed to page, is opened to change a branch protection rule.

**Nothing detects drift.** A plan runs when a human runs it. #1132 is the
worked example: main's ruleset drifted from its declared shape, no diff could
show it because nothing diffed, and the failure surfaced as unmergeable
branches rather than an alert. Moving the ruleset into tofu removed the
excuse; it did not add the detection.

## Decision: tofu-controller, auto-apply on main, all six roots

Use `flux-iac/tofu-controller` rather than building a reconciler. It is the
Flux ecosystem's OpenTofu controller (the former Weave TF-Controller), so the
`Terraform` CR is reconciled from the same `GitRepository` Flux already
watches, by the same controller-runtime machinery, with the same
`Kustomization` dependency ordering.

Viability checked 2026-07-26 rather than assumed: not archived, v0.16.4
released 2026-06-08, commits landing the same day the check was run, ~1.7k
stars, and a roughly monthly release cadence through 2026.

The mechanisms map onto the three failures directly:

| Failure | Mechanism |
| --- | --- |
| Second control plane | `Terraform` CR reconciled by Flux from the repo |
| Undetected drift | Continuous drift detection with periodic re-plan |
| Custody per apply | `runnerPodTemplate` env from an ESO-materialized Secret |
| Approval is a shell session | `approvePlan: auto` — the merged PR is the approval |

**The approval artifact is the merge to main.** Every root runs
`spec.approvePlan: auto`: a change to a root applies when its PR lands, the
same way every other change to this repository takes effect. The PR is
reviewable, attributable, revertable, and gated by the ruleset — the
properties an approval step needs, supplied by the step we already have.

Rejected, with reasons:

- **Per-plan manual approval (`approvePlan: <plan-id>`)** — livelocks in
  this monorepo. Verified in controller source: the plan id derives from the
  source artifact revision (`plan-<branch>-<sha[:10]>`), and before honoring
  a manual approval the controller re-plans at the current head and requires
  the fresh id to match the approved one. The approval commit itself
  advances head, so a git-based approval self-invalidates forever — and
  even a kubectl-based approval races main's velocity (Kargo pin pushes,
  Renovate). A dedicated promotion branch as the tf source would dodge this
  at the cost of a second ref to reason about; ruled out in favor of
  keeping it simple.
- **Atlantis** — PR-comment-driven applies hairpin cluster administration
  through GitHub, which is the specific thing the coding guidelines forbid.
- **Crossplane `provider-terraform`** — a second resource model layered on a
  second tool, to run the tool we already run.
- **A hand-rolled Job runner** — we would own state locking, plan storage,
  drift scheduling, and the approval protocol. The standing directive is to
  use existing tooling; this is exactly the case it was written for.

## All six roots move

Ruled 2026-08-08: there is no operator tier. Every root reconciles
in-cluster, ordered by blast radius, each with the guards that make auto
safe for it:

- `guardian-stripe-sandbox` — sandbox blast radius; the first mover.
- `guardian-mgmt-edge-policy` — its token cannot move traffic by
  construction.
- `guardian-github` — **split before migration.** The root's classic
  `repo`+`admin:org` PAT existed for exactly one resource:
  `github_app_installation_repository.promotions_homebrew_tap`, the write
  GitHub accepts from no App token and no fine-grained PAT. That binding
  leaves OpenTofu and becomes an owner-UI-managed fact recorded in
  `github-apps.md`, the same class as App installations themselves, which
  were already UI-only. Everything else — main's ruleset, the tap
  repository, the customer fleet — is managed in-cluster with **two
  per-org fine-grained PATs** (fine-grained PATs are scoped to a single
  resource owner): repository Administration on `guardian-intelligence`,
  repository create/administer on `digital-guardian-software`. The
  org-admin classic PAT dies with the split; cluster compromise no longer
  reaches org administration.
- `guardian-mgmt-dns` — auto like the rest. Sound because the reconcile
  loop (Flux to github.com, the controller to api.cloudflare.com)
  traverses nothing this root manages, so a bad apply cannot sever its own
  revert path: the worst case is a minutes-long ops-access window while a
  git revert converges. When the cluster is truly down the controller is
  down with it, so incident-time manual Cloudflare changes stick exactly
  when they are needed. Zones are data sources, not resources. Guards:
  `lifecycle.prevent_destroy` on the k8s-API records, the LB pool and LBs,
  and CAA; `check` blocks converted to preconditions (checks are warn-only
  in OpenTofu); a written pause procedure; no automerge for Cloudflare
  provider bumps touching this root.
- `guardian-mgmt-cloudflare-tokens` — the minter root. **This is the
  informed override of the never-in-cluster doctrine, ruled 2026-08-08**
  with the cost stated: a controller compromise can mint Cloudflare tokens,
  including R2-write credentials. Why it is acceptable: the minter token is
  revocable in one console action from the account login, which stays in
  the operator vault (tier 1) — the cluster holds a lever, not the root of
  trust; every mint is a state change a drift plan surfaces; offline
  custody pulls mean even an R2-wipe mint cannot destroy the recovery
  path. Guards: the runner namespace is CNP-walled to the Cloudflare API,
  drift detection runs on the shortest interval of any root (a hostile
  mint shows up as drift), and the root's expiry `check` becomes a
  precondition so a plan cannot silently pass a dead token forward.
- `guardian-mgmt` — the metal. Last, because it is the substrate the
  controller runs on. Auto is bounded by `lifecycle.prevent_destroy` on
  every server resource — a destroy plan fails the apply and alerts — and
  metal changes are rare, deliberate PRs. Cold boot is unaffected: with no
  cluster there is no controller, and the disaster path applies this root
  from a workstation per `cold-boot-bootstrap.md` with credentials from
  the operator vault, not from custody.

## Credentials and state

**Runtime (the controller): OpenBao, the documented path.** The controller
namespace gets the standard `guardian-reader`/`guardian-writer` pair and a
`ClusterSecretStore`; each root's `Terraform` CR mounts an
ExternalSecret-materialized Secret into the runner pod: the root's provider
token(s), the R2 backend keypair, and the state passphrase. All of it is
reissuable third-party material — exactly what `secrets.md` classes as
OpenBao content. Adding the namespace pair is a structural OpenBao change
(self-init is the sole source of truth), so it rides the one migration
ceremony below.

**Disaster recovery: the operator vault, not a new tier.** The bootstrap
set (tier 3) is not built for tofu, and nothing here needs it. The only
value that cannot be re-issued from a console is the state-encryption
passphrase — ciphertext outlives every token — so the passphrase is
**dual-homed**: OpenBao for runners, the operator vault for cold boot. The
cluster never reads the vault, so dual-homing crosses no boundary; it is
the same arrangement the custody repository passphrase already has. Every
other input (Latitude token, Cloudflare minter, R2 keypair) is re-issuable
from its console at DR time, which is the accepted worst case `secrets.md`
already states.

**One passphrase, not two.** The 2026-07-26 two-passphrase split existed
only because the tier boundary put one passphrase where the cluster could
never read it. With the tiering gone, a single fresh passphrase serves all
six roots. Migration is a one-time rekey per root: state pull under the
custody passphrase, push under the new one.

**`custody.env`'s tofu values retire.** The custody bundle's third opener —
the bootstrap-root apply — dies with the workstation path. Custody opens
for disaster recovery and key rotation, as designed, and the tofu values
(`tofu_state_encryption_passphrase`, provider tokens, R2 keypair) leave the
bundle manifest.

## The tiering that was ruled out

The 2026-07-26 ruling kept `guardian-mgmt` and
`guardian-mgmt-cloudflare-tokens` on a workstation forever: reconciling the
substrate from inside the substrate breaks the circularity DR exists for,
and the minter was doctrine-bound to never exist in-cluster. Their
credentials were to move to a then-unbuilt tier-3 bootstrap set. Ruled out
2026-08-08, deliberately: the circularity argument confused the routine
path with the disaster path (cold boot always applies from a workstation —
that recipe survives as the break-glass runbook, not as a tier), and the
minter's risk is bounded by console revocation plus drift detection rather
than by keeping a human in the loop of every routine apply. The tier-3
build it required stays unbuilt; the operator vault covers DR with fewer
moving parts.

## Preconditions

Each must hold before the step it gates; verified findings from the
2026-07-26 adversarial review carry forward unchanged.

- **R2 state locking, before the first migrated root.** No `versions.tf`
  sets `use_lockfile` today — safe while exactly one operator applies,
  unsafe the moment the controller and a workstation can both touch state.
  Enable it on every root and verify the lockfile's conditional-write
  semantics actually hold against R2 before trusting it.
- **Runner egress CNPs, before the first migrated root.** Runner pods need
  egress to GitHub, the Cloudflare API, R2, and Stripe, or they wedge as
  `POLICY_DENIED`.
- **Plan storage in secret mode.** `storeReadablePlan: human` writes plans
  to ConfigMaps, and the `read` persona can read ConfigMaps — the view role
  excludes only Secrets. Use secret/json mode.
- **Version lockstep.** The runner image defaults its own `TOFU_VERSION`
  (1.12.1 at last check) independent of the repo pin (1.12.5, multitool).
  Assert the pair in `version_skew_test.go`; a silent skew plans
  differently than a workstation does.
- **Provider API load.** Every reconcile plans with refresh, so each
  interval hits that root's provider APIs. Choose reconcile intervals with
  provider rate limits in mind.
- **`allowBreakTheGlass` stays off.** Break-glass is the workstation apply
  recipe — written down as a runbook *before* the final step deletes the
  routine workstation path, or the escape hatch dies with it.

## Migration

0. Install the controller with no `Terraform` CRs. Verify runner TLS
   issuance and that nothing reconciles.
1. Enable and verify R2 state locking across all roots; land the runner
   egress CNPs.
2. **The one ceremony** (single operator session, then never again):
   re-initialize OpenBao with the controller namespace pair
   (`openbao-static-seal-self-init.md`); generate the new state passphrase
   into the operator vault and OpenBao; per `custody.md`, open the bundle
   once to rekey each root's state to the new passphrase and relay the
   remaining credential values into OpenBao through scoped writer tokens;
   mint the two fine-grained GitHub PATs in the org consoles.
3. `guardian-stripe-sandbox`: plan-only soak, then flip to auto.
4. `guardian-mgmt-edge-policy`: same sequence.
5. `guardian-github`: land the split (the App-installation binding moves to
   `github-apps.md` as a UI-managed fact), then the same sequence on the
   two fine-grained PATs.
6. `guardian-mgmt-dns`, with its guards landed first.
7. `guardian-mgmt-cloudflare-tokens`, with its guards; then `guardian-mgmt`
   with `prevent_destroy` across the metal.
8. Write the break-glass workstation recipe, then delete the workstation
   path: drop the `TF_ENCRYPTION` assembly from routine docs, remove the
   tofu values from the custody manifest, and narrow the cold-boot
   runbook to the operator-vault credential path.

Step 8 is the deliverable. Until it lands this has added a system without
removing one, and custody still opens for ordinary work.

While the custody manifest shrinks, the bundle's three optional reissuable
GitHub App PEMs (`keys/*.private-key.pem`) belong in OpenBao by the same
argument that moved them out of the provisioning skills. They are not part
of this design; they are the obvious neighbouring cleanup.

## Risks

- **The controller is the most privileged workload in the cluster.** It
  holds every root's token, including the Cloudflare minter. Accepted by
  the 2026-08-08 ruling with these bounds: per-root token scoping (the
  GitHub PATs cannot reach Cloudflare; edge-policy cannot move traffic),
  a non-cluster-admin ServiceAccount, a CNP-walled runner namespace,
  secret-mode plan storage, and console-revocable credentials whose root
  of trust (the account logins) never leaves the operator vault.
- **Auto-apply on a bad plan.** With no human between merge and apply,
  `prevent_destroy` on the irreplaceable resources (metal, k8s-API
  records, LBs, CAA) is the backstop: a destructive plan fails the apply
  and alerts instead of executing.
- **Drift detection versus incident-time manual changes.** A manual
  Cloudflare change made during an incident would be reverted by the next
  reconcile. The pause procedure (suspend the CR) exists for exactly this
  and gates the DNS migration — and when the cluster is down enough to
  force manual DNS work, the controller is down too, so the change sticks
  until the cluster returns.
- **A hostile or accidental mint.** The minter root's drift plan is the
  detector; the console revocation from the vault-held account login is
  the response; offline custody pulls bound the damage of an R2-wipe
  mint.

# OpenTofu through GitOps: retiring the workstation apply

Status: ruled 2026-07-26 (auto-apply on main; root tiering), doc updated
2026-08-08. Not built. Prerequisite for shrinking custody to its
irreducible members.

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

## Decision: tofu-controller, auto-apply on main

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

**The approval artifact is the merge to main.** Every migrated root runs
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

## Which roots move

The split is by whether the cluster can safely reconcile something the
cluster itself depends on.

**GitOps tier — all four auto-applied:**

- `guardian-stripe-sandbox` — sandbox blast radius; the first mover.
- `guardian-mgmt-edge-policy` — its token cannot move traffic by
  construction, which bounds the worst case.
- `guardian-github` — no circularity and a loud, safe consumer, but its
  token is ruleset-admin over the repository that defines the cluster: a
  controller compromise could drop the ruleset, push, and let Flux apply
  the result. **Open decision before this root migrates:** split it into a
  fleet-repos root (in-cluster) and a guardian-repo ruleset root (operator
  tier), or accept the exposure explicitly.
- `guardian-mgmt-dns` — auto like the rest, migrated last. Auto is sound
  here because the reconcile loop (Flux to github.com, the controller to
  api.cloudflare.com) traverses nothing this root manages, so a bad apply
  cannot sever its own revert path: the worst case is a minutes-long
  ops-access window (the `guardian_mgmt_k8s_api` records and the edge LB
  hostnames) while a git revert converges. When the cluster is truly down
  the controller is down with it, so incident-time manual Cloudflare
  changes stick exactly when they are needed. Zones are data sources, not
  resources — the unrecoverable object is not managed here. The guards
  that make this safe are listed under migration.

**Operator tier — applied from a workstation, permanently:**

- `guardian-mgmt-cloudflare-tokens` — the minter token is Account API Tokens
  Write, root-equivalent, and doctrine is that it never exists in-cluster.
- `guardian-mgmt` — provisions the metal the controller runs on. At cold boot
  you need the Latitude token to build the substrate that would run the
  controller that holds the Latitude token; reconciling the substrate from
  inside the substrate is the circularity DR exists to break.

**Where an apply runs and where its credential lives are separate
questions.** These two roots are applied by an operator forever. That does
not make them custody material, and treating the two axes as one is what put
`custody.env` in the bundle in the first place.

Their credentials belong in **tier 3, the bootstrap set** (`secrets.md`):
one age-encrypted object per value in R2, encrypted to the repo-committed
recipient key. Writing or rotating one is a blind write — encrypt to a public
key and upload, no passphrase and no bundle open. Decryption happens at
disaster recovery and drills, using the age identity from the operator vault,
which is precisely why the minter still never exists in-cluster: the cluster
can read the ciphertext and decrypt nothing.

So no tofu root needs the custody bundle. The GitOps tier reads OpenBao, the
operator tier reads the bootstrap set, and `custody.env` leaves the bundle
entirely — which finishes the custody shrink to its irreducible members
(Talos genesis, `talm.key`, `talosconfig`, the LINSTOR master passphrase, and
the OpenBao seal metadata).

**Prerequisite: tier 3 is documented but not built.** There is no age or
bootstrap-set tooling in `src/infrastructure/cmd/`, and `custody.env` is
still the only home for these values. Building it is a precondition for the
operator tier's half of this design, not a config change. It gates nothing
in the GitOps tier: the controller and all four migrated roots run entirely
on OpenBao through ESO, the documented path for reissuable credentials.

## Credentials and state

Each migrated root gets an `ExternalSecret` in the controller's namespace
projecting its provider token and the R2 backend keypair, mounted into the
runner pod as env. Those values move `custody.env` → OpenBao, which is
correct independent of this design: all of them are reissuable from their
owning console, and `secrets.md` already classes reissuable third-party
credentials as OpenBao material.

State encryption takes **two passphrases, not one**, and this falls out of
the tier boundary rather than a general rule about duplication:

- GitOps tier — a new passphrase generated into OpenBao, recovered with the
  rest of the estate by raft snapshot restore. It cannot live in the
  bootstrap set, because reading that requires the operator age identity,
  which decrypts root-equivalent tokens; the cluster must never hold it.
- Operator tier — a bootstrap-set object. It cannot live in OpenBao, because
  at cold boot there is no cluster to serve it.

Neither passphrase can be moved to the other's home, so one shared passphrase
is not available even in principle. Migrating a root is a one-time rekey:
state pull under the old passphrase, push under the new.

## Preconditions from review

Findings from the 2026-07-26 adversarial review, verified against the
controller source and this repository; each must hold before the step it
gates.

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
  Assert the pair in `version_skew_test.go` or build a custom runner; a
  silent skew plans differently than a workstation does.
- **Provider API load.** Every reconcile plans with refresh, so each
  interval hits that root's provider APIs. Choose reconcile intervals with
  provider rate limits in mind.
- **`allowBreakTheGlass` stays off.** Break-glass is the workstation apply
  recipe — which must be written down as a runbook *before* the final step
  deletes the routine workstation path, or the escape hatch dies with it.

## Migration

0. Install the controller with no `Terraform` CRs. Verify runner TLS
   issuance and that nothing reconciles.
1. Enable and verify R2 state locking across all roots, and land the runner
   egress CNPs.
2. `guardian-stripe-sandbox`: rekey to the OpenBao passphrase, soak
   plan-only, then flip to auto.
3. `guardian-mgmt-edge-policy`: same sequence.
4. `guardian-github`: resolve the ruleset-split decision, then the same
   sequence.
5. `guardian-mgmt-dns`, last, with its guards in place first:
   `lifecycle.prevent_destroy` on the k8s-API records, the LB pool and LBs,
   and CAA, so a would-be destroy becomes a failed apply and an alert; the
   root's `check` blocks converted to preconditions (checks are warn-only
   in OpenTofu and block nothing); a written pause/suspend procedure for
   incident-time manual changes; and no automerge for Cloudflare provider
   bumps that touch this root. Then plan-only soak, then auto.
6. Build the bootstrap set (tier 3), and move the operator tier's provider
   tokens, R2 backend keypair, and state passphrase into it. Independent of
   the controller — it can run in parallel with steps 0–5.
7. Write the break-glass workstation recipe, then delete the workstation
   path for migrated roots: drop their `TF_ENCRYPTION` references, empty
   `custody.env` out of the bundle manifest, and narrow the cold-boot
   runbook's "OpenTofu state encryption" section to the operator tier
   reading its passphrase from the bootstrap set.

Steps 6 and 7 are the deliverable. Until they land this has added a system
without removing one, and custody still opens for ordinary work.

While `custody.env` is being emptied, the bundle's three optional reissuable
GitHub App PEMs (`keys/*.private-key.pem`) belong in OpenBao by the same
argument that moved them out of the provisioning skills. They are not part of
this design; they are the obvious neighbouring cleanup.

## Risks

- **The controller becomes privileged.** It holds provider tokens for four
  roots. Mitigation is that the tokens are individually scoped — edge-policy
  cannot move traffic, the GitHub token cannot reach Cloudflare — and its
  ServiceAccount is not cluster-admin. The `guardian-github` token is the
  outlier; that is why its migration waits on the ruleset-split decision.
- **Auto-apply on a bad plan.** With no human between merge and apply, the
  DNS `prevent_destroy` guards and preconditions are the backstop: a
  destructive plan fails the apply and alerts instead of executing.
- **Drift detection versus DR.** A manual change made during an incident on
  `guardian-mgmt-dns` would be reverted by the next reconcile. The pause
  procedure (suspend the CR) exists for exactly this and gates step 5 —
  and when the cluster is down enough to force manual DNS work, the
  controller is down too, so the change sticks until the cluster returns.

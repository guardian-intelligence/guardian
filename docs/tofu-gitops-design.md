# OpenTofu through GitOps: retiring the workstation apply

Status: proposed, 2026-07-26. Not built. Prerequisite for shrinking custody
to its irreducible members.

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

## Decision: tofu-controller

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
| Approval is a shell session | `spec.approvePlan: <plan-id>` — approving is a commit |

That last row is the one that makes this worth doing. The controller plans,
publishes the plan id, and refuses to apply until the CR names that exact id.
Naming it is a PR against main — reviewable, attributable, revertable, and
gated by the same ruleset as every other change. The approval becomes an
artifact instead of an action.

Rejected, with reasons:

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

**GitOps tier:**

- `guardian-github` — no circularity, live consumer, safest first mover.
- `guardian-stripe-sandbox` — sandbox blast radius; the one candidate for
  `approvePlan: auto`.
- `guardian-mgmt-edge-policy` — its token cannot move traffic by
  construction, which bounds the worst case.
- `guardian-mgmt-dns` — moves, but **never** auto-approved: it is the one
  root that can sever the cluster's own reachability.

**Custody tier — a design point, not debt:**

- `guardian-mgmt-cloudflare-tokens` — the minter token is Account API Tokens
  Write, root-equivalent, and doctrine is that it never exists in-cluster.
- `guardian-mgmt` — provisions the metal the controller runs on. Reconciling
  the substrate from inside the substrate is the circularity DR exists to
  break.

These two keep the ceremony permanently. Custody still opens for them — just
never for routine work, and never on a schedule set by ordinary PRs.

## Credentials and state

Each migrated root gets an `ExternalSecret` in the controller's namespace
projecting its provider token and the R2 backend keypair, mounted into the
runner pod as env. Those values move `custody.env` → OpenBao, which is
correct independent of this design: all of them are reissuable from their
owning console, and `secrets.md` already classes reissuable third-party
credentials as OpenBao material.

State encryption takes **two passphrases, not one**:

- GitOps tier — a new passphrase generated into OpenBao, recovered with the
  rest of the estate by raft snapshot restore.
- Custody tier — keeps `tofu_state_encryption_passphrase`.

Migrating a root is a one-time rekey: state pull under the old passphrase,
push under the new. Sharing a single passphrase across both tiers was
rejected because a secret duplicated into a lower tier inherits the weaker
tier's exposure, and the entire point is that the GitOps tier's key may live
somewhere the cluster can read.

## Migration

0. Install the controller with no `Terraform` CRs. Verify runner TLS issuance
   and that nothing reconciles.
1. `guardian-github`, explicit approval. Prove plan-only first, then a single
   approved apply. Its consumer complains loudly and safely if this is wrong.
2. Rekey and migrate `guardian-stripe-sandbox` and
   `guardian-mgmt-edge-policy`.
3. `guardian-mgmt-dns`, manual approval only.
4. Delete the workstation path for migrated roots: drop their `TF_ENCRYPTION`
   references, shrink `custody.env`, and narrow the cold-boot runbook's
   "OpenTofu state encryption" section to the custody-tier roots.

Step 4 is the deliverable. Until it lands, this has added a system without
removing one.

## Risks

- **The controller becomes privileged.** It holds provider tokens for four
  roots. Mitigation is that the tokens are individually scoped — edge-policy
  cannot move traffic, the GitHub token cannot reach Cloudflare — and its
  ServiceAccount is not cluster-admin.
- **Version skew.** The runner image pins its own OpenTofu version,
  independent of the repo pin. That pair needs an assertion in
  `version_skew_test.go`; a silent skew would plan differently than a
  workstation does.
- **Drift detection versus DR.** A manual change made during an incident on
  `guardian-mgmt-dns` would be reverted by the next reconcile. Manual
  approval blunts this; the pause procedure needs writing before phase 3.

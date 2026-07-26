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
operator tier's half of this design, not a config change.

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

## Migration

0. Install the controller with no `Terraform` CRs. Verify runner TLS issuance
   and that nothing reconciles.
1. `guardian-github`, explicit approval. Prove plan-only first, then a single
   approved apply. Its consumer complains loudly and safely if this is wrong.
2. Rekey and migrate `guardian-stripe-sandbox` and
   `guardian-mgmt-edge-policy`.
3. `guardian-mgmt-dns`, manual approval only.
4. Build the bootstrap set (tier 3), and move the operator tier's provider
   tokens, R2 backend keypair, and state passphrase into it. Independent of
   the controller — it can run in parallel with steps 0–3.
5. Delete the workstation path for migrated roots: drop their `TF_ENCRYPTION`
   references, empty `custody.env` out of the bundle manifest, and narrow the
   cold-boot runbook's "OpenTofu state encryption" section to the operator
   tier reading its passphrase from the bootstrap set.

Steps 4 and 5 are the deliverable. Until they land this has added a system
without removing one, and custody still opens for ordinary work.

While `custody.env` is being emptied, the bundle's three optional reissuable
GitHub App PEMs (`keys/*.private-key.pem`) belong in OpenBao by the same
argument that moved them out of the provisioning skills. They are not part of
this design; they are the obvious neighbouring cleanup.

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

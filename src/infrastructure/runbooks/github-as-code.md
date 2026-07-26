# GitHub as code — main's ruleset, the Homebrew tap, and the simulated customer fleet

`src/infrastructure/bootstrap/guardian-github` describes the GitHub objects
that gate merges, the Homebrew tap the postflight-cli release cutter mirrors
its formula to, and the repositories the simulated customer fleet runs in.
Everything below is copy-paste executable.

## Why this root exists

On 2026-07-24, #1132 collapsed every PR check into one `build-and-test`
workflow. main's ruleset still required `build`, `derive`, `site-perf-gate`,
and `shortty-smoke-gate` — workflows that no longer existed, so those contexts
could never report. Every branch cut after the rename was permanently
unmergeable. Nothing caught it, for two compounding reasons:

- the ruleset lived only in the GitHub UI, so no diff could show the drift;
- an unreportable required check sits **pending**, not failed, so the PR is
  BLOCKED with a green check list and nothing pages.

`//src/infrastructure/tests:tests` now holds `local.required_check` equal to
the gate workflow's job key, and the fleet map equal to the repositories the
canary loop drives. Renaming either side fails at PR time.

## Credentials

The provider reads `GITHUB_TOKEN`; it is never a tofu variable and never
lands in state. The token needs `repo` and `admin:org` on **both**
organizations — `guardian-intelligence` for the ruleset,
`digital-guardian-software` for the fleet. The platform GitHub App cannot be
used here: its installation token is scoped to ghcr reads.

Applying this root is an operator ceremony, not an agent task: it needs the
state passphrase and a token no in-cluster identity holds. Assemble the
environment once per session per
[cold-boot-bootstrap.md](cold-boot-bootstrap.md), "OpenTofu state
encryption", then `aspect infra tofu-init --root guardian-github`. That
section is the only place the recipe lives; do not re-derive it here.

This root's state is encrypted from its first write, so `versions.tf` carries
no `unencrypted` fallback — the migration ceremony in
[cold-boot-bootstrap.md](cold-boot-bootstrap.md) does not apply to it.

The ceremony is a known defect, not the target state: hand-run applies make
this root a second control plane beside Flux, and the credentials it needs
are what keep custody in routine circulation. The cutover to in-cluster
reconciliation is designed in [`docs/tofu-gitops-design.md`](../../../docs/tofu-gitops-design.md).

## First run is an import, not an apply

The ruleset and the fleet repositories predate this root. A plan that
proposes to *create* any of them means the import did not happen — stop and
fix that, never let it create a second `main-protection`. The Homebrew tap
is the one object born here: a create for `github_repository.homebrew_tap`
and its App grant is the expected plan until the first apply, and drift
afterwards.

```sh
cd src/infrastructure/bootstrap/guardian-github

tofu import github_repository_ruleset.guardian_main guardian:19596988

for repo in postflight-canary simulated-customer-node simulated-customer-go \
            simulated-customer-python simulated-customer-gradle; do
  tofu import "github_repository.customer_fleet[\"$repo\"]" "$repo"
done
```

**The gate: `tofu plan` must report no changes before any apply.** Anything
else is drift between this file and reality — reconcile the file to reality
first, then change reality through the file.

## Adding a repository to the fleet

One entry in `local.customer_fleet`, one entry in the canary loop's Job list
in `deployments/postflight-runner/canary-loop.yaml`. The lockstep test fails
if you do one without the other. `tofu apply` creates the repository; the
canary loop starts driving it on the next reconcile.

Repositories carry `prevent_destroy`. They accumulate the pull-request history
the billing showback is reconciled against, so removing one from the map is
not enough to delete it — that is deliberate. To retire a repository, remove
it from both files, then delete it by hand and `tofu state rm` the address.

## The Homebrew tap

`brew install guardian-intelligence/tap/postflight` resolves because the
repository is named `homebrew-tap`; the release cutter's stable path writes
`Formula/postflight.rb` there with a tap-scoped `guardian-promotions` token.
Both halves live in this root: the repository and the App-installation grant
that makes the token mintable. If a stable cut fails at its
`create-github-app-token` step naming `homebrew-tap`, this root has not been
applied — apply it, then re-run the cutter with `workflow_dispatch`. The
formula is machine-written: to change what it says, change
`dist/homebrew/postflight.rb.tmpl` and cut a release, never the tap.

## Changing the required check

Rename the job in `.github/workflows/build-and-test.yml` and
`local.required_check` in the same PR. CI fails otherwise. After merge, apply
this root — until you do, the ruleset still requires the old name and main is
merge-locked.

Recovering a ruleset someone edited by hand:

```sh
gh api repos/guardian-intelligence/guardian/rulesets            # list
gh api repos/guardian-intelligence/guardian/rulesets/19596988/history
gh api repos/guardian-intelligence/guardian/rulesets/19596988/history/<version_id>
```

`gh api repos/.../branches/main/protection` returns **404 Branch not
protected** for a ruleset-protected branch. That 404 means "not classic branch
protection", not "unprotected" — do not read it as the latter.

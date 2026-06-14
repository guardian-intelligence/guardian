# aisucks release runbook

Customer-grade: every step is a copy-paste command with an expected outcome.
The release artifact is a **git tag** — hermetic Bazel makes the image digest
a pure function of the commit, and step 4 verifies that instead of trusting it.

The old workflow-owned automated form has been removed. The steps below are the
manual service-image release spec until release tooling invoked through
`aspect` replaces them. Package-owned release state machines own package
projection details, while shared release infrastructure owns evidence and
admission shape. The ratified successor (`docs/architecture/release.md`) moves
deploys to per-cluster Flux + a release judge, with GitHub holding no cluster
credentials; the gate criteria below carry over as the judge's soak spec.
Rollback (step 5) is manual either way. The release record IS the annotated tag
set: `git tag -n1 -l 'aisucks/v*'` lists every release with its digest.

Public OCI vending is a separate release target; see
`docs/runbooks/public-release.md`. The npm SDK uses the OCI-first projection lane in
`docs/runbooks/npm-sdk-release.md`.

Sites: dev `206.223.228.101` (vs-dev-w0) · gamma `45.250.254.119` (gd-gamma-w0)
· prod `67.213.115.113` (gd-prod-w0). CAUTION: `206.223.228.87` and
`206.223.228.99` are **verself's** gamma/prod boxes (the no-touch list in
AGENTS.md) — never target them with anything in this runbook.

Conventions for every step below, run from the repo root:

```sh
export KUBECONFIG=~/.local/state/guardian/guardian-<site>/kubeconfig
# The ~/.local/bin tool shims need runfiles when invoked outside bazelisk run:
export RUNFILES_DIR="$(bazelisk info bazel-bin 2>/dev/null)/src/guardian-cli/cmd/guardian/guardian_/guardian.runfiles"
```

## PR previews (dev.aisucks.app)

Dev serves whatever the workspace last converged — that IS the preview
mechanism. To preview a branch:

```sh
git checkout <branch>
bazelisk run //src/guardian-cli/cmd/guardian:guardian -- up src/sites/dev/site.yaml
```

One preview at a time; converging main puts dev back. No CI hook yet — when a
forge with PRs exists, the hook is exactly this command.

## 0. Secret projection

The observability stack's `grafana-admin` Kubernetes Secret is projected
from `kv/guardian/<site>/observability/grafana-admin` in OpenBao. `guardian
up` generates the value on a fresh Bao and waits for the projection before
applying Grafana. Never run `kubectl create secret generic grafana-admin`
by hand.

Config-bearing observability components (otel-collector, alertmanager) do
NOT restart on ConfigMap-only changes — after editing site.yaml watch lists
or rotating the ntfy topic, `kubectl -n observability rollout restart
deploy/otel-collector deploy/alertmanager`. vmalert reloads rule files
itself (-configCheckInterval).

## 1. Cut

From a clean checkout of main, all tests green, then tag:

```sh
git status --short            # expect: empty
bazelisk test //...           # expect: all PASSED
git tag aisucks/v<N>
```

Migrations discipline (checked at review, enforced by no one else):
the current aisucks skeleton has no product database. When product state returns,
schema changes must be additive-only — the previous binary must run against
the new schema, or step 5 (rollback) is a lie.

## 2. Converge gamma

```sh
bazelisk run //src/guardian-cli/cmd/guardian:guardian -- up src/sites/gamma/site.yaml
```

Record the line `pushed registry.guardian.internal/aisucks@sha256:…` —
that digest is the release. Put it in the annotated tag:

```sh
git tag -f -a aisucks/v<N> -m "aisucks@sha256:<digest>"
```

## 3. Gate (against gamma)

```sh
H=https://gamma.aisucks.app
curl -fsS -o /dev/null -w 'healthz %{http_code} in %{time_total}s\n' $H/healthz   # expect: 200
# Match the charter-locked promise text (verbatim, changes only by charter
# amendment) — never a marketing string a redesign can drop.
curl -fsS $H/ | grep -q 'never be sold' && echo page ok                            # expect: page ok
```

Any failed expectation stops the release. Fix forward on dev; never ship a
tag that didn't gate green.

## 4. Promote to prod

Same tag, same workspace, no commits in between:

```sh
git describe --exact-match --tags   # expect: aisucks/v<N>
bazelisk run //src/guardian-cli/cmd/guardian:guardian -- up src/sites/prod/site.yaml
```

**Assert the pushed aisucks digest is byte-identical to gamma's.** If it
differs, STOP: the build is not reproducible — that is a bug to fix before
anything ships. Re-run the gate checks against prod.

## 5. Rollback

```sh
git checkout aisucks/v<N-1>
bazelisk run //src/guardian-cli/cmd/guardian:guardian -- up src/sites/prod/site.yaml
git checkout main
```

Works because the previous tag's digest re-derives from its commit. Once
product state returns, the additive-migrations rule in step 1 is also required.

# aisucks release runbook

Customer-grade: every step is a copy-paste command with an expected outcome.
The release artifact is a **git tag** — hermetic Bazel makes the image digest
a pure function of the commit, and step 4 verifies that instead of trusting it.

Automated form: `.github/workflows/release.yml` runs steps 1–4 end-to-end on
every merge to main (self-hosted runner: docs/runbooks/release-runner.md) and
cuts the tag after the prod gate, so only green releases get tags. The steps
below remain the spec the workflow apes, and the manual path when GitHub or
the runner is down. Rollback (step 5) is manual either way. The release
record IS the annotated tag set: `git tag -n1 -l 'aisucks/v*'` lists every
release with its digest.

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

## 0. One-time per site (operator)

```sh
kubectl create namespace aisucks --dry-run=client -o yaml | kubectl apply -f -
kubectl label namespace aisucks pod-security.kubernetes.io/enforce=privileged --overwrite
PASS=$(openssl rand -hex 24)
kubectl -n aisucks create secret generic aisucks-db \
  --from-literal=password="$PASS" \
  --from-literal=url="postgres://aisucks:$PASS@postgres.aisucks.svc.cluster.local:5432/aisucks"
```

The password is never written anywhere else; losing it means recreating the
secret and restarting both pods (postgres keeps the old credential in PGDATA —
`ALTER USER` via `kubectl exec` if the database must be kept).

The observability stack needs its own one-time secret (grafana sits in
CreateContainerConfigError until it exists — PodNotReady will page):

```sh
kubectl -n observability create secret generic grafana-admin \
  --from-literal=password="$(openssl rand -hex 24)"
```

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
**additive-only** — the previous binary must run against the new schema,
or step 5 (rollback) is a lie.

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
curl -s -o /dev/null -w 'garbage -> %{http_code}\n' -X POST -d 'link=https://evil.example/share/x' $H/report   # expect: 422
# Canary submission (requires the canary share links, docs/runbooks/canaries.md).
# Resubmits render the same LOGGED page as first submits (membership-oracle defense).
curl -s -X POST --data-urlencode "link=$CANARY_CHATGPT" $H/report | grep -q 'LOGGED' && echo canary ok
kubectl -n aisucks exec postgres-0 -- psql -U aisucks -t -c 'select count(*) from reports;'  # expect: >= 1
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
anything ships. Re-run the gate's first three checks against prod.

## 5. Rollback

```sh
git checkout aisucks/v<N-1>
bazelisk run //src/guardian-cli/cmd/guardian:guardian -- up src/sites/prod/site.yaml
git checkout main
```

Works because migrations are additive-only (step 1) and the previous tag's
digest re-derives from its commit.

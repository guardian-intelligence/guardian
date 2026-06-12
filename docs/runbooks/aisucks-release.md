# aisucks release runbook

Customer-grade: every step is a copy-paste command with an expected outcome.
The release artifact is a **git tag** — hermetic Bazel makes the image digest
a pure function of the commit, and step 4 verifies that instead of trusting it.

Automated form: `.github/workflows/release.yml` runs steps 1–4 end-to-end on
every merge to main (self-hosted runner: docs/runbooks/release-runner.md) and
cuts the tag after the prod gate, so only green releases get tags. The steps
below remain the spec the workflow apes, and the manual path when GitHub or
the runner is down. Rollback (step 5) is manual either way. Automated releases
record their digest in the annotated tag (`git tag -n1 -l 'aisucks/v*'`); the
table below continues for human-notable entries.

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
# amendment) — marketing copy once vanished in a redesign and broke the check.
curl -fsS $H/ | grep -q 'never be sold' && echo page ok                            # expect: page ok
curl -s -o /dev/null -w 'garbage -> %{http_code}\n' -X POST -d 'link=https://evil.example/share/x' $H/report   # expect: 422
# Canary submission (requires the canary share links, docs/runbooks/canaries.md).
# v5+: resubmits render the same LOGGED page as first submits (membership-oracle fix).
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

## Release record

| tag | date | aisucks digest | gated on | notes |
|---|---|---|---|---|
| aisucks/v1 | 2026-06-11 | `sha256:db72c805ada1ef524be6e7b66954b1c369bed4571fa8d664f7fe9d0b372efb12` | dev (gamma pending M1) | first cut: page, healthz, ingest with liveness check |
| aisucks/v2 | 2026-06-11 | `sha256:c5509e7e8e3c22f5c88bc15d2ba32074fcbfc26b1107931b6749a34b4ea0e202` | gamma (canary stored: gpt-5-5-thinking, 2 turns) | flight-format parser; ChatGPT-only launch scope; CA bundle baked in |
| aisucks/v3 | 2026-06-11 | `sha256:dadc557ca13f90768a03fe6ac1917f1e7de299d6fbb645b7de51449aff45d275` | gamma | /healthz on :80 in domain mode (readiness fix); prod NIC flip (f0) |
| aisucks/v4 | 2026-06-11 | `sha256:165770b15dd7dec49c30858fcf2d5c4d00e353cc54500bd2ee43504c2768e893` | gamma → prod (digest match asserted via deploy specs) | zero-downtime deploys (SO_REUSEPORT + hostNetwork rolling); LAUNCH: aisucks.app live, browser E2E green (type + Enter → verdict), TTFB 0.06–0.19s |
| aisucks/v5 | 2026-06-11 | `sha256:0f16aa75b2d7d92a7048a7e49e8e3cbc4f6730091bb37a495061c291f23dc32e` | gamma → prod | SECURITY: close corpus-membership oracle — present and absent links return an identical LOGGED page (verified byte-identical on gamma; canary resubmit on prod says LOGGED not ALREADY). Zero-downtime rollout. KNOWN ISSUE: ChatGPT soft-404s (200) let fabricated UUIDs park as parse_failed instead of bouncing — anti-abuse follow-up. |
| aisucks/v6 | 2026-06-11 | `sha256:e0da747e47ee71c0daa85aa27125f3ca08e3555e3d5f09bacce2cbb143a9aaa6` | gamma → prod | Request hardening: bounce ChatGPT soft-404s (fabricated UUIDs no longer park; verified DOESN'T ANSWER on gamma+prod), cap POST body at 4KB (413), pin method/path. Gatus page check now matches the charter promise text, not "EVIDENCE LOCKER" (the TanStack redesign dropped it, causing false page-down alerts on all envs while healthz stayed green — not a downtime event). MIGRATION NOTE: dev deadlocked because it was upgraded in place across the hostPort→hostNetwork transition (old hostPort portmap collided with new SO_REUSEPORT bind); fixed by scale 0→1. Fresh-from-maintenance converges (gamma/prod) were unaffected. All sites now uniformly hostNetwork, so the hazard is passed. |
| aisucks/v7 | 2026-06-12 | `sha256:d8303253b2f563b2714d3ddc78fccfabfad58db563d4d0396ba519328da3559c` | gamma → prod (digest byte-identical) | OBSERVABILITY: app floor (loopback /metrics+pprof, /livez + startupProbe over the 5-min DB wait, slog JSON, GOMEMLIMIT+limits fleet-wide, stdlib ErrorLog silenced — client IPs) + hot plane on all sites (VM 13mo, vmalert 8 rules, Alertmanager→ntfy ?template=alertmanager PROVEN, otel-collector, ksm, blackbox sibling probes, grafana port-forward-only). PAGE-PROOF on dev: synthetic, PodRestartStorm (induced crash-loop), SiteProbeFailed (induced gamma outage — cross-site death detection via the NEW pipeline = Gatus retirement condition 1 of 2 met; dead-man heartbeat still pending). ClickHouse authored, deploys in the ledger release. |
| aisucks/v8 | 2026-06-12 | `sha256:ca255af0aef15c0bb8c99b4383211b42f0d9816f4fa5b0249adca1084d880f1a` | gamma → prod (digest byte-identical) | INSTRUMENTS (M1): app RED {listener,handler,method,code} + duration histogram + inflight; funnel completed (429s counted); fetch/parse dependency metrics with reason at branch points; pgxpool gauges; Converged events from `guardian up` (verified live, digests in message); grafana deploy dashboard (generation-based markers); rules 8→10 (AppErrorRate, UpstreamFetchDegraded with activity floor + p95 arm). CHARTER v2 promise copy live (Expert human annotators; 'never be sold' marker intact). D4.v3 drill: healthz-503 injection did NOT page (exclusion fixed in review — original matcher was dead), 502 injection fired AppErrorRate on schedule. Built by 9-agent ultracode workflow; 3-lens review found 4 real defects, all fixed pre-ship. |

# Runbook: the observability ledger (per-site ClickHouse) bring-up

Brings up the per-site ledger — ClickHouse plus the otel-collector's logs
tee (filelog container logs + k8sobjects k8s Events) — on a site whose
environment bundle sets `platform.clickhouse.enabled: true`. Customer-grade: every step is a
command against the real site, in order, with its verification. The ratchet
is dev → gamma → prod; never start a site before the previous one's verify
section passes.

Scope of the current slice (roadmap M5, first two sub-items): container
logs and Events only. No OTLP receiver, no app-SDK traces, no R2 backup
CronJobs yet — those are the M5 remainder.

Setup for every step (repo root, per site):

```sh
export KUBECONFIG=~/.local/state/guardian/guardian-<site>/kubeconfig
export RUNFILES_DIR="$(bazelisk info bazel-bin 2>/dev/null)/src/guardian-cli/cmd/guardian/guardian_/guardian.runfiles"
```

## 1. Converge

`guardian up` owns the admin Secret path. On a fresh OpenBao it generates
`kv/guardian/<site>/observability/clickhouse-admin`, installs External
Secrets Operator, applies the projection, and waits for the Kubernetes
`clickhouse-admin` Secret before applying ClickHouse. On a restored OpenBao
the value must already exist in the restored snapshot; missing restored
paths are a schema migration, not a hand-created Kubernetes Secret.

Never run `kubectl create secret generic clickhouse-admin` by hand. That
would split truth between Kubernetes and OpenBao and would be lost on a
wipe/restore.

```sh
bazelisk run //src/guardian-cli/cmd/guardian:guardian -- up src/sites/<site>/bootstrap.yaml
```

Paging hygiene: the converge restarts the collector (`Recreate` strategy) —
a one-scrape-interval metrics gap; no monitored endpoint is in the path and
nothing approaches the 3×30s gatus threshold. clickhouse is new, so nothing
alerts on it yet. First collector start ingests the kubelet's retained log
backlog (~10Mi × containers); memory_limiter backpressures the brief blip.

## 2. Apply the DDL by hand

The exporter runs `create_schema: false`; the schema is the reviewed,
vendored DDL in `src/infrastructure-components/clickhouse/k8s/ddl/`, never
whatever the exporter would CREATE. Idempotent (`CREATE ... IF NOT EXISTS`
everywhere); future migrations append numbered files.

```sh
kubectl -n observability exec -i deploy/clickhouse -- \
  clickhouse-client --password "$(kubectl -n observability get secret clickhouse-admin -o jsonpath='{.data.password}' | base64 -d)" \
  --multiquery < src/infrastructure-components/clickhouse/k8s/ddl/0001_otel.sql
```

DDL-after-collector is fine: the exporter retries; allow ~1 minute after
this step before judging the verify queries.

## 3. Verify

Query helper (used by everything below):

```sh
chq() { kubectl -n observability exec -i deploy/clickhouse -- \
  clickhouse-client --password "$(kubectl -n observability get secret clickhouse-admin -o jsonpath='{.data.password}' | base64 -d)" \
  -q "$1"; }
```

- Logs flowing — `chq "SELECT count() FROM otel.otel_logs"` rising across
  two invocations.
- Per-source spread —
  `chq "SELECT ResourceAttributes['k8s.namespace.name'], count() FROM otel.otel_logs GROUP BY 1"`
  shows every running namespace (the filelog receiver is selective about
  streams, never width).
- Events present —
  `chq "SELECT count() FROM otel.otel_logs WHERE ScopeName LIKE '%k8sobjects%'"`
  nonzero, and the converge marker:
  `chq "SELECT Body FROM otel.otel_logs WHERE ScopeName LIKE '%k8sobjects%' AND Body LIKE '%Converged%' LIMIT 1"`
  — etcd forgets in 1h; the ledger remembers, proven. Two gotchas, both
  observed on the dev bring-up: keep the ScopeName filter (ClickHouse's own
  console logs are ingested, so an unscoped `Body LIKE '%Converged%'`
  matches the verify query itself), and remember watch mode only emits
  events created AFTER the collector started — if the step-1 converge
  finished before the collector's first clean start, re-run the (idempotent)
  converge to mint a fresh Converged event.
- Identity stamp —
  `chq "SELECT DISTINCT ResourceAttributes['k8s.cluster.name'] FROM otel.otel_logs"`
  returns exactly `guardian-<site>`.
- Collector health — `kubectl -n observability logs deploy/otel-collector |
  grep -i error` quiet, and `up{job="otelcol-self"} == 1` plus
  prometheusremotewrite still flowing in VM (the config is one file: a bad
  ledger key would have killed the metrics pipeline with it — this check is
  the gate before touching the next site).
- Disk watch (repeat occasionally; single-node log volume is small today,
  but ClickHouse's own console logging is itself ingested) —
  `chq "SELECT formatReadableSize(sum(bytes_on_disk)) FROM system.parts WHERE database='otel'"`.

## 4. Prod note

Prod's `clickhouse.enabled` is `false` and its converge is deferred to the
M5 prod step. Before flipping it, confirm the OpenBao snapshot/restore path
contains `kv/guardian/guardian-prod/observability/clickhouse-admin` or run
the explicit Bao schema migration path. Do not pre-create the Kubernetes
Secret. With the flag off, prod's collector renders byte-identical to the
metrics-only spine and prod deploys no clickhouse objects.

## Standing rules

- Everything a pod writes to stdout/stderr is now shipped telemetry on
  ledger sites. Charter source-discipline applies at the emitter: no client
  IPs, no chat/transcript content, ever — fix the emitter, never add a
  scrubber. Any NEW component needs a what-it-logs review before it lands.
- Product services must not log request bodies, client IPs, user-provided
  URLs, or SDK payloads. The current aisucks skeleton has no product database
  and no user-write path; reintroducing either requires a what-it-logs review.
- Wipe drills erase `/var/lib/otel-collector` (filelog checkpoints) and
  `/var/lib/clickhouse-data` together — both EPHEMERAL by design; ledger
  history dies with the drill until the R2 backup sub-item lands. Collector
  restarts re-deliver recent Events (k8sobjects watch re-list); dedupe at
  query time.

# Observability architecture

The app-side floor and the hot plane are live fleet-wide and page-proven by
drill (induced crash-loop and induced cross-site outage both paged). The
forensics tier's first slice is deployed (amended 2026-06-12): per-site
ClickHouse (`src/infrastructure-components/clickhouse/`, in push.go behind
the site-gated `clickhouse.enabled` flag — ON dev+gamma, OFF prod until its
Secret exists) with container logs + k8s Events flowing
(docs/runbooks/ledger.md); OTLP/app-SDK traces and R2 backups remain M5.
This doc is the core primitive set; it changes by amendment.

## Values that constrain the design

1. **Open standards only.** OpenTelemetry (OTLP) as the instrumentation +
   collection spine; PromQL / OpenMetrics / `remote_write` for metrics; W3C
   Trace Context for propagation. No vendor-proprietary agents or wire formats.
2. **Minimal footprint.** Four pods on one node per env. Pick the lowest-RAM
   implementation of each standard; avoid clustered/HA variants.
3. **The privacy charter applies to telemetry (charter value 2).** No client
   IP and no chat/transcript content may land in logs, traces, span
   attributes, or the obs store — the obs store is in-scope for "no personal
   data," same as the corpus. HTTP auto-instrumentation violates this by
   default (records `client.address`, URLs, bodies); scrubbing is a
   first-class, tested requirement, not an afterthought.

## Two planes — they do not share a store

The decision that anchors everything: **availability detection never runs
through ClickHouse.** CH is columnar OLAP — superb for "scan N million spans,"
wrong for "is prod down right now" (async inserts, merges, query overhead).

- **Hot / alerting plane (seconds, in-memory rule eval).** This *is* the
  "stream processor" people reach for — it already has boring names.
  Whitebox: **VictoriaMetrics single-node + `vmalert`** (PromQL alerting +
  recording rules, Alertmanager-compatible). Blackbox: **Gatus** (already
  deployed, self-contained threshold eval → ntfy). A general-purpose stream
  engine (Flink/RisingWave/Materialize/Arroyo) is unjustified at this scale;
  do not add one. For continuous derived signals later, prefer ClickHouse
  Materialized Views (incremental aggregation at insert) over a stream engine —
  but MVs are still not the alerting path.
- **Cold / forensics plane (ClickHouse).** Raw logs + traces, high-cardinality
  history. Queried *after* a page, human-driven, so CH latency is irrelevant
  and its scan speed is the point.

## Component choices and rationale

- **OTel Collector** (mirror verself's `otelcol-contrib`) per node, as the spine
  and the tee: scrapes cAdvisor + kube-state-metrics + app `/metrics` (prometheus
  receiver), receives OTLP; derives RED metrics from spans (`spanmetrics` /
  `count` connectors); **scrubs PII** (default-deny attribute allowlist via
  `transform`/`redaction` processors); fans out — derived metrics → VM, raw
  logs/traces → CH. Because the Collector scrapes and remote-writes, we run
  **neither Prometheus nor vmagent**.
- **VictoriaMetrics (single binary, NOT clustered)** for metrics + alerting.
  Chosen over Prometheus purely for footprint: single static Go binary,
  several times less RAM at equal ingestion, better compression, no WAL bloat,
  degrades gracefully under the high-cardinality Cilium/Hubble metrics. It is
  **not a standards deviation** — VM speaks PromQL, OpenMetrics, `remote_write`,
  native OTLP metric ingestion, and the Alertmanager protocol; only the storage
  engine differs. `vmalert` runs the rules; Alertmanager (or a thin webhook)
  routes to ntfy.
- **ClickHouse** for logs + traces. This is the "CH for everything" intent,
  honestly scoped: CH serves everything its ecosystem supports (logs, traces —
  cf. SigNoz/Uptrace/HyperDX). The **only** deviation is the metrics+alerting
  hot path, because CH has no low-latency PromQL/Alertmanager story.
  **Per-site CH, not central.** Each site owns
  its ledger: the collector exports over cluster-local transport (no new
  ingest port through the ingress firewall, no telemetry on the WAN), and a
  site's forensics never depend on another box. Costs accepted: three
  backup/PITR streams to R2, per-site TTL + disk budgeting (the wide-event
  tables dominate the NVMe long before VM does), CH memory caps so the
  ledger never competes with postgres/app, Grafana fans out over three
  datasources for cross-site questions.
- **Grafana** PER SITE, in-cluster, port-forward access (no new public
  surface). Cross-site questions are two tabs; `Distributed` tables exist if
  three sites ever stops being a number a human can fan out over.
- **Selectivity:** CH is selective about STREAMS, never WIDTH —
  admitted events arrive with full context; the gates are privacy (the
  default-deny attribute allowlist) and disk (flow export off, probe/health
  spans dropped at the collector, debug logs sampled). VM is promiscuous
  about metrics and ruthless about labels (bounded dimensions only).
  Retention: VM 13 months (one global knob, YoY comparisons always covered);
  CH per-table TTL starting at 90d for raw spans/logs, expanded on evidence —
  the long-horizon record is R2, not live TTL.
- **Cross-site rule:** no cross-site COUPLING, ever —
  no shared state, no internal dependencies between sites. The single
  permitted cross-site act is OBSERVATION of public surfaces (blackbox
  probes of another site's https endpoints, indistinguishable from a
  visitor), because the death-detection invariant demands an observer
  outside the failure domain and a sibling site is the only candidate that
  adds no new vendor or surface.
- **Alerting converges on ONE pipeline**: vmalert →
  Alertmanager → ntfy. Blackbox coverage moves into it — each site runs
  blackbox_exporter probing the OTHER sites' public endpoints (the invariant:
  a site-local whitebox stack cannot report its own node's death; at least
  one prober must sit outside the failure domain). **Gatus retires** once
  cross-site blackbox-in-VM has caught a drill-induced outage AND a
  dead-man heartbeat exists for the pipeline itself (an always-firing
  Watchdog alert whose ABSENCE pages — the whitebox stack cannot report its
  own death, and `up == 0` requires the scraper, TSDB, and evaluator all
  alive to fire). Until both: proven scaffolding.

## Correlation = trace ID, via W3C Trace Context

Do not invent a parallel correlation-ID scheme. The OTel SDK starts a span at
the edge; `traceparent` propagates the trace ID through every hop
(auto-injected/extracted by instrumentation). The **trace ID is the
correlation ID** — it appears on every log line (slog ↔ trace correlation) and
span, so "everything for this request across services" is one trace-ID filter
in CH. The "next sprint" task is to wrap the typed SDK clients with an OTel
interceptor (a `RoundTripper`/middleware that starts-or-continues a span and
injects `traceparent`); then propagation is automatic and no ID is hand-rolled.

## The layers (floor and hot plane live; forensics next)

1. **App-side floor** (live): resource
   requests/limits + `GOMEMLIMIT` so OOMKills are cgroup-scoped and reported as
   `OOMKilled`; `/metrics` (client_golang) + `/debug/pprof` on loopback only;
   structured `slog` (trace correlation arrives with the SDK); `/livez` liveness
   behind a startupProbe sized to outlast the app's deliberate 5-minute postgres
   wait (a bare liveness probe would kill-loop every cold boot). On Talos,
   `talosctl dmesg | grep -i oom` is the node-level OOM confirmation path (no SSH).
2. **Hot plane** (live): OTel Collector + VictoriaMetrics + vmalert +
   Alertmanager(→ntfy via the native `?template=alertmanager` rendering),
   kube-state-metrics, cAdvisor scrape.
3. **Forensics tier** (next; roadmap M5): ClickHouse + the OTel logs/traces
   pipeline, with the PII scrubbing as a tested default-deny allowlist;
   Grafana over both.

## Standing constraints from operations

- **Dashboards must make node-level faults unmistakable**: node boot/uptime
  and per-namespace restart counts sit side by side on the first dashboard —
  lockstep restarts across namespaces mean the node, not a workload.
  Blackbox-only monitoring once mis-attributed a node fault to the app.
- **`replicas` stays 1 until the Gateway lands** — two hostNetwork pods
  would share 127.0.0.1:9090 via SO_REUSEPORT, scrapes would interleave two
  counter sets under one series identity, and `rate()` reads the flips as
  resets. The edge gateway (`docs/architecture/gateway.md`) moves the app to
  pod network with per-pod scrape identity, which unblocks `replicas: 2`;
  OTLP push with per-pod resource attributes (ledger release) is the other
  path.
- Still open from the Cilium workstream: Gateway API, default-deny
  CiliumNetworkPolicies, and the `toFQDNs` egress lockdown — design in
  `docs/architecture/gateway.md`, deferral notes in
  `src/infrastructure-components/cilium/values.yaml`. Hubble metrics already
  flow into VM.

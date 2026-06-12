# Observability architecture

Status (2026-06-12): the app-side floor and the hot plane are SHIPPED
fleet-wide (aisucks/v7) and page-proven by drill — induced crash-loop paged
PodRestartStorm, induced gamma outage paged SiteProbeFailed cross-site. The
forensics tier (ClickHouse + logs/traces pipelines) is authored
(`src/infrastructure-components/clickhouse/`, held out of push.go) and
deploys in the ledger release. Designed 2026-06-11. This doc is the core
primitive set; it changes by amendment.

## Why this exists

We debugged a live prod crash-loop (aisucks pod, exit 255, ~8 restarts) with
`kubectl logs --previous` (keeps only the last dead container), `exec cat
/proc` (point-in-time, can't see a climb), and `describe` (said "Unknown"
because no resource limits → no `OOMKilled` reason). We could not answer "did
memory grow before the kill" because nothing recorded it. The gap is not a
better kubectl incantation; it is that no telemetry was flowing before the
incident. We have **blackbox** monitoring (Gatus: "is it up from outside"); we
lack **whitebox** observability ("why is it unhealthy from inside").

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
  **AMENDED 2026-06-12 (operator): per-site CH, not central.** Each site owns
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
- **Selectivity (2026-06-12):** CH is selective about STREAMS, never WIDTH —
  admitted events arrive with full context; the gates are privacy (the
  default-deny attribute allowlist) and disk (flow export off, probe/health
  spans dropped at the collector, debug logs sampled). VM is promiscuous
  about metrics and ruthless about labels (bounded dimensions only).
  Retention: VM 13 months (one global knob, YoY comparisons always covered);
  CH per-table TTL starting at 90d for raw spans/logs, expanded on evidence —
  the long-horizon record is R2, not live TTL.
- **Cross-site rule (2026-06-12, operator):** no cross-site COUPLING, ever —
  no shared state, no internal dependencies between sites. The single
  permitted cross-site act is OBSERVATION of public surfaces (blackbox
  probes of another site's https endpoints, indistinguishable from a
  visitor), because the death-detection invariant demands an observer
  outside the failure domain and a sibling site is the only candidate that
  adds no new vendor or surface.
- **Alerting converges on ONE pipeline** (amended 2026-06-12): vmalert →
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

## Sequencing (each step independently useful; never half-migrated)

1. **App-side floor** (SHIPPED, v7): resource
   requests/limits + `GOMEMLIMIT` so OOMKills are cgroup-scoped and reported as
   `OOMKilled`; `/metrics` (client_golang) + `/debug/pprof` on loopback only;
   structured `slog` (trace correlation arrives with the SDK); `/livez` liveness
   behind a startupProbe sized to outlast the app's deliberate 5-minute postgres
   wait (a bare liveness probe would kill-loop every cold boot). On Talos,
   `talosctl dmesg | grep -i oom` is the node-level OOM confirmation path (no SSH).
2. **Hot plane** (SHIPPED, v7): OTel Collector + VictoriaMetrics + vmalert +
   Alertmanager(→ntfy via the native `?template=alertmanager` rendering),
   kube-state-metrics, cAdvisor scrape. This is the real paging upgrade — a
   `kube_pod_container_status_restarts_total` rule pages on a crash-loop, which
   blackbox watch-0 only caught incidentally and late.
3. **Forensics tier:** ClickHouse + the OTel logs/traces pipeline, with the PII
   scrubbing as a tested default-deny allowlist; Grafana over both.

## Open threads this doc depends on / defers to

- **Cilium edge work LANDED first (2026-06-11)**: CNI + kube-proxy
  replacement + Hubble converted on all three sites (wipe drills;
  `docs/runbooks/cilium-conversion.md`), Hubble metrics scraped into VM as
  designed. Still open from that workstream: Gateway API (listener strategy
  vs the app's host :80/:443), default-deny CiliumNetworkPolicies, and the
  `toFQDNs` egress lockdown — see
  `src/infrastructure-components/cilium/values.yaml` for the deferral notes.
- **Incident RESOLVED 2026-06-11** (and the leak hypothesis above it
  falsified): the "aisucks crash-loop" was never the app. Gamma and prod
  nodes were warm-rebooting every ~75 minutes — the machine config never
  declared the `zfs` kernel module, so `ext-zfs-service` waited forever on
  `/dev/zfs`, the Talos boot sequence never completed (stage stuck
  `booting`), and machined reboot-retried on timeout. Fixed by the
  `zfs-module.yaml` talos patch on all three sites. Diagnostic trail: BMC SEL
  empty (ruled out watchdog/power), lockstep restart counts across every pod
  including the control plane (ruled out any single workload), machined logs
  named the wedged task. **Lesson for this doc:** blackbox-only monitoring
  mis-attributed a node-level fault to the app for a full day; the hot
  plane's first dashboard should put node boot/uptime and per-namespace
  restart counts side by side, exactly so lockstep restarts are unmissable.
  The floor shipped in v7 (limits + `GOMEMLIMIT`, `/livez` + startupProbe).
  **`replicas` stays 1 deliberately** — two hostNetwork pods would share
  127.0.0.1:9090 via SO_REUSEPORT, scrapes would interleave two counter sets
  under one series identity, and `rate()` reads the flips as resets. It
  returns to 2 when the app pushes OTLP with per-pod resource attributes
  (ledger release).

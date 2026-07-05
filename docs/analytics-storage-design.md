# Analytics event storage: schema bake-off and decision

**Date:** 2026-07-04 · **Status:** decided (storage schema + wire contract); ClickHouse deployment is a separate change
**Artifacts:** wire contract `src/proto/guardian/analytics/v1/events.proto` · DDL `src/infrastructure/analytics/events-table.sql` · harness `src/infrastructure/analytics/bench/` (regenerates every number below: `gen.sql` then `bench.sh <port>`)

## Context

One pipeline carries both business analytics and fraud/abuse signals: a single
`Event` shape with a server-assigned trust tier, W3C trace context for joining
server traces, ClickHouse as the store. This doc records the measured bake-off
that fixed the storage layout and the wire contract, per the repo rule that
component choices carry recorded reasons and the standing gap that gates and
canaries should record real throughput numbers, not just pass/fail. The
numbers were adversarially re-verified by an independent review pass that
reproduced them against the live lab (and falsified four of the first draft's
claims — corrected below).

## Method

20M synthetic events, ~29 days, generated in ClickHouse 26.3.2 (local lab,
32 cores): zipfian paths/visitors/UAs (400k visitors), near-monotonic arrival
timestamps, /24-clustered IPv4 + 6% IPv6, per-network country/ASN, event mix
52.4% page_view / 20.4% web_vital / ~11% trace-joining (rpc+error) / clicks,
scrolls, outbound. Identity attributes (ip/ua/geo/asn) are **visitor-stable
with ~4% network churn** — an earlier run drew them per-event and understated
the winning layout by 35%; compression numbers from decorrelated data are
fiction. Sizes from `system.parts`, active parts, after `OPTIMIZE FINAL`.

Caveat: synthetic data flatters LowCardinality columns somewhat (60 paths,
108 UAs; production cardinality is higher). Revisit triggers below guard the
assumptions that could invalidate the layout.

## Results — 20M events, settled parts (results_variants.tsv)

| variant | layout | disk | B/event | vs naive |
|---|---|---|---|---|
| v0 naive | String-typed dump, LZ4, ORDER BY ts | 1.75 GiB | 94.0 | 1.0x |
| v1 typed | real types + LowCardinality, LZ4 | 1.21 GiB | 65.1 | 1.4x |
| v2 codec | v1 + per-column codecs (ZSTD(1), Delta, T64, Gorilla) | 1.02 GiB | 55.0 | 1.7x |
| v3 deep ORDER BY | v2, ORDER BY (site,event,path,corr,ts) | 1.04 GiB | 56.0 | 1.7x |
| v4 ts ORDER BY | v2, ORDER BY (server_ts) | 1.04 GiB | 55.7 | 1.7x |
| v5 zstd3 | v2 with ZSTD(3) | 1.02 GiB | 55.0 | 1.7x |
| v6 zstd6 | v2 with ZSTD(6) | 1.01 GiB | 54.3 | 1.7x |
| v7 lean | semantic schema (below), event-type ORDER BY | 633 MiB | 33.2 | 2.8x |
| v8 lean, visitor ORDER BY, LC correlation | control for the winner | 323 MiB | 16.95 | 5.5x |
| **shipping DDL** | **v8 with plain FixedString(16) correlation** | **319 MiB** | **16.71** | **5.6x** |
| v9 lean+day+visitor | v8 ORDER BY with day bucket inserted | 575 MiB | 30.2 | 3.1x |

Wire form (JSONEachRow as a Publish handler receives it): 628.6 B/event →
**37.6x wire-to-disk**. CH-internal uncompressed→compressed ratio 6.18.

## What actually mattered (in order)

1. **Random IDs are the storage budget.** In v2, `correlation_id` (16B, ratio
   1.00), `trace_id` (16B), `span_id` (8B) were 40 of 55 B/event — the other
   19 columns *combined* were 15B. No codec compresses randomness; the levers
   are semantic:
   - `span_id` **dropped** — span granularity belongs to the tracing backend;
     analytics needs only the trace join key.
   - `trace_id` **sparse by contract** — populated only for events that join a
     server trace (rpc/error, ~11% of volume): 16 → 2.0 B/event.
   - `client_ts` (absolute) → **server-derived `client_skew_ms Int32`**
     (0.42 B/event vs 0.97 delta-coded absolute in v2, 1.64 under the
     visitor ORDER BY): server arrival is the only clock that orders events.
2. **ORDER BY locality beats every codec — and does the correlation_id work.**
   Visitor clustering (site, correlation_id, server_ts) is worth 16 B/event
   over the identical columns ordered by event type (33.2 → 16.7): a
   visitor's ip/ua/paths become locally constant, and correlation_id itself
   compresses 16 → 1.37 B/event as *plain FixedString runs* (ratio ~11.7).
   The first draft credited a LowCardinality dictionary for that; the review
   A/B falsified it — LC measures 1.61 B/event under the same ORDER BY and
   *worse* than plain everywhere else, so the shipping DDL uses plain
   FixedString(16) and carries no dictionary-spill regime at all.
   Day-bucketing the ORDER BY (v9) destroys the effect — 29 day-buckets
   dilute each visitor to ~1.7-event runs. Clustering must span the partition.
3. **ZSTD level is a non-lever, and RECOMPRESS TTL is a no-op here.** ZSTD(3)
   saved 0.0%, ZSTD(6) ~2.7% at up to 2x merge cost. The first draft's
   cold-tier plan (`TTL ... RECOMPRESS ZSTD(6)`) was cut after live
   verification: recompression TTL skips columns with explicit codecs — on
   this DDL it is a scheduled full-part rewrite with zero byte change. Cold
   recompression, if ever worth 2.7%, is an `ALTER ... MODIFY COLUMN` job.
4. **Typing is the free 31%.** v0→v1 with zero tuning; the naive dump also
   bloats every downstream cache and merge forever.

## Throughput (32-core lab, server co-resident; recorded per repo practice)

| path | rate |
|---|---|
| INSERT SELECT, in-server transform (storage ceiling) | 12.5M rows/s |
| JSONEachRow → `input()` transform → shipping table, 1 client | **1.60M events/s** (~1.0 GB/s wire) |
| same, 8 parallel clients | **3.17M events/s** (~2.0 GB/s wire) |
| query: daily uniq IPs, one path, 7d window | 40 ms |
| query: 3-step windowFunnel over all visitors | 54 ms (event-ordered v7: 240 ms) |
| query: one visitor's full history | 17 ms (v7: 51 ms) |

JSON parsing bounds ingest (expected); a single stream still sustains ~138B
events/day against current real volume of ~10²/day — the Connect handler and
network will saturate long before ClickHouse. The visitor ORDER BY is ~4x
faster on the fraud/funnel archetypes and pays ~20ms on time-slice aggregates
at this scale; the projection escape hatch is recorded in the DDL.

**Ingest batching is mandatory**: with time-ordered arrival, freshly written
parts run ~2.2x the settled size (37.8 B/event measured on 20k-row batches)
until merges settle them; settled monthly partitions dominate steady-state
disk. The ingest service must buffer (≥100k rows or seconds-scale flush,
`async_insert` acceptable) rather than insert per request.

## Decision

Shipping DDL: lean semantic schema, plain `FixedString(16)` correlation key,
per-column codecs at ZSTD(1), `ORDER BY (site, correlation_id, server_ts)`,
monthly partitions, 25-month delete TTL, `trust_tier` Enum8 with
`'unspecified' = 0` (an insert that omits the column must not default into
the highest-trust tier).

## Wire contract (reference-grounded)

The proto is the intersection of what Segment (track/common spec), PostHog
(capture API), Plausible (events API) and the OTel event/LogRecord model
agree on:

- **Clients send**: event name, path, referrer, props, per-event `offset_ms`,
  batch-level `sent_at` — the minimal common set across the references.
- **Server derives; no wire fields exist**: receipt time, IP, UA, geo, ASN,
  trust tier, and clock skew (`received_at − sent_at`, the Segment/PostHog
  pattern — clients never compute skew or send absolute times).
  Implementation state (ingest v1): geo/ASN derivation is deferred (needs an
  MMDB source + refresh story), `country`/`asn` stay zero until then;
  `offset_ms` is accepted on the wire but not yet stored — `server_ts` is
  batch receipt time, in-session order comes from `session_seq`, and the
  field is reserved for skew-corrected event time.
- **Identity is the HMAC correlation cookie**, not a body field — unlike
  Segment's client-settable `anonymousId`, ours is server-minted and
  verified; a body-level id would be a second, weaker channel.
- **Dedup/replay**: `(correlation_id, session_seq)` is a deterministic dedup
  key and gap detector — stronger than the references' random
  `messageId`/`insert_id` (which need a 1–7-day server-side ID store) and
  free: both columns already exist. Wire `uint32` matches storage `UInt32`.
- **Traces**: `trace_id` absent/all-zero = joins no server trace (OTel
  logs.proto semantics). Web-vital fields mirror `browser.web_vital`.
- **Producer matrix**: the Publish RPC fills the client-claimed columns;
  `status`/`duration_ms` are filled by the server-observed producer (request
  logs → collector) and stay 0 on client-emitted rows.
- **Enforcement is value-level, not unknown-field-level**: stock connect-go's
  JSON codec hardcodes `DiscardUnknown`, so unknown fields vanish rather than
  reject. The ingest layer must enforce: event-name registry, batch-size cap
  (reject, don't truncate), props byte cap, and exact 16-byte `trace_id`
  (ClickHouse FixedString silently zero-pads short values and aborts the
  insert block on long ones). These are implementation requirements recorded
  here because the proto cannot express them.
- The OTel ClickHouse exporter's own DDL (attribute maps + bloom-filter
  crutches + materialized hot columns) is the argument *for* dedicated typed
  columns when the schema is first-party: it promotes typed columns out of
  its maps for every hot key. If a long-tail property ever needs indexing,
  the escape hatch is one `Map(LowCardinality(String), String)` overflow
  column, not a redesign.

## As-deployed verification (in-cluster CHI, 2026-07-05)

The whole vertical is live in `guardian-analytics` (raw Altinity CHI,
`clickhouse-server:26.3.17.4`, 1×3 ReplicatedMergeTree over a 3-host CHK
quorum). Every lab number reproduced on the deployed image, and the
operational gates passed against the live cluster:

- **Compression** re-run on the deployed image against the same 20M
  generator: **16.71 B/event exact** (334,105,129 B / 20M, ratio 6.18) —
  identical to the lab. Insert of 20M rows in 5.7 s.
- **Ingest throughput** (single stream, JSONEachRow through the wire form,
  in-pod localhost socket, 4-CPU limit): **1.22M events/s**.
- **Replica-kill test**: 60 × 20k acknowledged batches with one passive and
  one active replica deleted mid-run; all three replicas converged to
  exactly `TOTAL_ACKED` (1,200,000) — zero acknowledged-row loss, zero
  duplicates.
- **End-to-end trust boundary** through the live `/api/events` ingress: a
  request via Cloudflare lands `edge_verified` with the real client IP and a
  server-minted correlation cookie that round-trips into `correlation_id`;
  a forged direct-to-origin request with spoofed `X-Guardian-Client-Ip` +
  `…-Source: cloudflare` lands `client_claimed` with an empty IP (the
  ingress strips the spoofed headers because no client cert verified). This
  is the load-bearing gate — the trust tier cannot be lied into.
- **Canonical queries** on 20M real-shaped rows, raw rows only (no
  pre-aggregation, no dashboard layer): funnel 0.098 s, visitor-history
  timeline 0.068 s, ASN abuse rollup 0.219 s, high-velocity-IP rollup
  0.328 s, per-path p75 LCP 0.068 s. Funnels and abuse views assemble
  directly from the event rows, as intended.
- **Retention**: a synthetic 26-month-old row does not survive the
  `INTERVAL 25 MONTH DELETE` TTL; current data is retained.
- **Self-observation** (no external alerting is deployed): ingest rate,
  unique visitors, and reject counts are all queryable from ClickHouse
  itself (hourly `count()` / `uniqExact(correlation_id)`); the ingest
  service logs per-reason reject counts structured.

Durability rests on app-level replication (ReplicatedMergeTree over Keeper)
on `local-retain` volumes: a pod loss re-syncs from a surviving replica, and
the PVCs survive pod deletion. A full backup/restore-into-scratch-namespace
drill is the remaining operational item, tracked for the DR runbook alongside
the CHK snapshot story.

## Traces (OTel spans in the same store)

The second consumer of this ClickHouse: OTLP spans, so a log line's
`correlation_id`/`trace_id` leads to the full trace in the same place we
query business analytics. An OTel Collector (contrib v0.155.0, digest-pinned)
receives OTLP and writes to `guardian_analytics.otel_traces` with
`create_schema: false` — the exporter's INSERT column set is matched exactly,
but the engine (ReplicatedMergeTree over Keeper), ORDER BY, codecs,
partitioning, TTL, and indexes are ours. ORDER BY is
`(ServiceName, toStartOfHour(Timestamp), TraceId, Timestamp)`: trace-adjacent
within each service/hour bucket, honoring the lab finding that guardian's
trace-correlated span attributes compress best when co-located by trace
(trace-adjacent 46.0 vs resource-first 61.5 B/span) while keeping
service-scoped scans cheap. A `otel_traces_trace_id_ts` table + MV give
trace_id → time-range lookup. 6-month TTL (higher volume, shorter value than
the 25-month business events).

**Verified live**: a synthetic OTLP span through the deployed collector
landed in the owned schema with `guardian.correlation_id` queryable as a
span attribute (the same id that keys the event rows — this is the join that
makes "follow a visitor from analytics into their server trace" one query),
and the lookup MV populated automatically.

**Revisit trigger (traces ORDER BY)**: the trace-adjacent choice was measured
on synthetic spans with lab-assumed attribute correlation. Re-measure B/span
on ≥1 week of real production spans once producers are emitting, and freeze or
adjust the ORDER BY with that evidence (hybrid `(service, hour, trace_id, ts)`
= 58.7 is the query-ergonomic fallback).

## Revisit triggers

- **UA cardinality**: if production UA distinct-count per part exceeds
  ~100k, demote `ua` to `String CODEC(ZSTD(1))`.
- **Time-slice queries at scale**: if month-partition scans for per-path
  aggregates exceed budget, add a (site, path, day) projection rather than
  reordering the base table.
- **Ingest batch discipline**: watch `system.parts` active-part counts once
  the ingest service exists; per-request inserts are a deploy blocker.
- **Props growth**: `props` is a capped JSON string (0.6 B/event today);
  evaluate the native JSON type only if it becomes load-bearing.

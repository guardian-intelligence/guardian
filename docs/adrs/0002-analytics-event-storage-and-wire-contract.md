# 0002 — Analytics event storage and wire contract

Status: Accepted · Date: 2026-07-04

## Context

One pipeline carries both business analytics and fraud/abuse signals, so the schema
must serve funnel/visitor-history/abuse queries directly from raw rows, join server
traces, and keep storage honest at high volume. A measured bake-off (20M synthetic
events, adversarially re-verified; harness at `src/infrastructure/analytics/bench/`,
`gen.sql` then `bench.sh <port>` regenerates every number) drove the layout. Three
findings dominated:

1. **Random IDs are the storage budget.** Correlation/trace/span IDs were 40 of
   55 B/event in the naive-typed layout; no codec compresses randomness. The levers
   are semantic: drop `span_id` (span granularity belongs to the tracing backend),
   populate `trace_id` only for events that join a server trace, and replace absolute
   client timestamps with server-derived skew.
2. **ORDER BY locality beats every codec.** Visitor clustering
   `(site, correlation_id, server_ts)` is worth ~2x over the same columns ordered by
   event type: a visitor's ip/ua/paths become locally constant and the correlation
   key compresses as plain FixedString runs — measured *better* than LowCardinality,
   so the DDL carries no dictionary regime. Day-bucketing the ORDER BY destroys the
   effect; clustering must span the partition.
3. **ZSTD level is a non-lever** (≤2.7% for up to 2x merge cost), and RECOMPRESS TTL
   skips columns with explicit codecs — a no-op on this DDL.

## Decision

- **Store**: ClickHouse. **Wire contract**:
  `src/proto/guardian/analytics/v1/events.proto`. **DDL**:
  `src/infrastructure/analytics/events-table.sql`.
- One `Event` shape with a server-assigned `trust_tier` (Enum8, `'unspecified' = 0`
  so an omitted column cannot default into the highest tier) and W3C trace context.
- Lean semantic schema: plain `FixedString(16)` correlation key, per-column codecs at
  ZSTD(1), `ORDER BY (site, correlation_id, server_ts)`, monthly partitions, 25-month
  delete TTL. Result: ~16.7 B/event, 5.6x over a naive typed dump.
- Clients send only the minimal cross-reference set (event name, path, referrer,
  props, `offset_ms`, batch `sent_at`); the server derives receipt time, IP, UA,
  geo/ASN, trust tier, and clock skew. Identity is the server-minted correlation
  cookie, never a body field (an unauthenticated UUID until HMAC signing lands with
  this namespace's OpenBao scope). `(correlation_id, session_seq)` is the declared
  dedup key and gap/replay detector on the wire; nothing deduplicates at rest today —
  the engine is plain MergeTree. Ingest enforcement is value-level (event-name
  registry, batch/props caps, exact 16-byte trace_id) because connect-go discards
  unknown JSON fields.
- **Ingest batching is mandatory** (≥100k rows or seconds-scale flush;
  `async_insert` acceptable): per-request inserts are a deploy blocker.
- **OTLP spans land in the same store** (`guardian_analytics.otel_traces`), exporter
  schema owned by us (`create_schema: false`): ReplicatedMergeTree,
  `ORDER BY (ServiceName, toStartOfHour(Timestamp), TraceId, Timestamp)`, 6-month
  TTL, trace_id→time-range lookup MV. The correlation id lands in both stores — an
  ORDER BY key in `events`, the `guardian.correlation_id` span attribute in
  `otel_traces` — so analytics→trace is one join, via the attribute map.

## Consequences

- Fraud/funnel/visitor-history archetypes run ~4x faster than an event-ordered
  layout; time-slice aggregates pay ~20ms — the escape hatch is a
  `(site, path, day)` projection, never reordering the base table.
- Long-tail properties get one `Map` overflow column if ever needed, not a redesign.
- Revisit triggers: UA distinct-count per part >~100k → demote `ua` to plain
  `String`; active-part counts under real ingest; `props` growth vs the native JSON
  type; traces ORDER BY re-measured on ≥1 week of real production spans.

Related source: `src/infrastructure/analytics/events-table.sql`,
`src/proto/guardian/analytics/v1/events.proto`,
`src/infrastructure/deployments/analytics/system/traces-configmap.yaml`

# Analytics event storage: schema bake-off and decision

**Date:** 2026-07-04 · **Status:** decided (storage schema + wire contract); ClickHouse deployment is a separate change
**Artifacts:** wire contract `src/proto/guardian/analytics/v1/events.proto` · DDL `src/infrastructure/analytics/events-table.sql`

## Context

One pipeline carries both business analytics and fraud/abuse signals: a single
`Event` shape with a server-assigned trust tier, W3C trace context for joining
server traces, ClickHouse as the store. This doc records the measured bake-off
that fixed the storage layout and the wire contract, per the repo rule that
component choices carry recorded reasons and per the standing gap that gates
and canaries should record real throughput numbers, not just pass/fail.

## Method

20M synthetic events, ~29 days, generated in ClickHouse 26.3.2 (local lab,
32 cores): zipfian paths/visitors/UAs (400k visitors), near-monotonic arrival
timestamps, /24-clustered IPv4 + 6% IPv6, per-network country/ASN, realistic
event mix (67% page_view, web vitals, rpc, clicks). Identity attributes
(ip/ua/geo/asn) are **visitor-stable with ~4% network churn** — an earlier run
drew them per-event and understated the winning layout by 35% (23 vs 17
B/event); compression numbers from decorrelated data are fiction. Sizes read
from `system.parts` after `OPTIMIZE FINAL`. Harness:
`src/infrastructure/analytics/bench/` (`gen.sql` + `bench.sh`).

Caveat: synthetic data flatters LowCardinality columns somewhat (60 paths, 108
UAs; production UA/path cardinality is higher). The revisit triggers below
guard the assumptions that could invalidate the layout.

## Results — 20M events, settled parts

| variant | layout | disk | B/event | vs naive |
|---|---|---|---|---|
| v0 naive | String-typed dump, LZ4, ORDER BY ts | 1.75 GiB | 94.0 | 1.0x |
| v1 typed | real types + LowCardinality, LZ4 | 1.21 GiB | 65.1 | 1.4x |
| v2 codec | v1 + per-column codecs (ZSTD(1), Delta, T64, Gorilla) | 1.02 GiB | 54.9 | 1.7x |
| v3 deep ORDER BY | v2, ORDER BY (site,event,path,corr,ts) | 1.04 GiB | 56.0 | 1.7x |
| v4 ts ORDER BY | v2, ORDER BY (server_ts) | 1.04 GiB | 55.7 | 1.7x |
| v5 zstd3 | v2 with ZSTD(3) | 1.02 GiB | 54.9 | 1.7x |
| v6 zstd6 | v2 with ZSTD(6) | 1.01 GiB | 54.3 | 1.7x |
| v7 lean | schema semantics (below), event-type ORDER BY | 633 MiB | 33.2 | 2.8x |
| **v8 lean+visitor ORDER BY** | **v7, ORDER BY (site, correlation_id, server_ts)** | **323 MiB** | **17.0** | **5.5x** |
| v9 lean+day+visitor | v7, ORDER BY (site, day, corr, ts) | 575 MiB | 30.2 | 3.1x |

Wire form (JSONEachRow as a Publish handler receives it): 628.6 B/event →
**37x wire-to-disk** for v8. CH-internal uncompressed→compressed ratio 5.23.

## What actually mattered (in order)

1. **Random IDs are the storage budget.** In v2, `correlation_id` (16B, ratio
   1.00), `trace_id` (16B), `span_id` (8B) were 40 of 55 B/event — the other
   19 columns *combined* were 15B. No codec compresses randomness; the only
   levers are semantic:
   - `span_id` **dropped** — span granularity belongs to the tracing backend;
     analytics needs only the trace join key.
   - `trace_id` **sparse by contract** — populated only for events that join a
     server trace (rpc/error, ~12% of volume): 16 → 2.0 B/event.
   - `correlation_id` → **LowCardinality(FixedString(16))** — visitors repeat
     (~50 events each), so the per-part dictionary turns 16 random bytes into
     ~1.6B of indices. (ClickHouse itself flags LowCardinality over numerics
     as suspicious; over FixedString identity keys with guaranteed repetition
     it is the right tool.)
   - `client_ts` (absolute, 8B Delta-coded ~1.0B) → **`client_skew_ms Int32`**
     (~0.1B), server-derived from the batch's `sent_at`: server arrival is
     the only clock that orders events.
2. **ORDER BY locality beats every codec.** v8's visitor clustering
   (site, correlation_id, server_ts) is worth 16 B/event over the identical
   column set ordered by event type (v7: 33.2 → 17.0): a visitor's ip/ua/
   paths/dictionary-indices become locally constant. Day-bucketing the same
   idea (v9) *destroys* it — 29 day-buckets dilute each visitor to ~1.7-event
   runs. Clustering must span the partition.
3. **ZSTD level is a non-lever.** ZSTD(3) saved 0.0%, ZSTD(6) 1.1%, at up to
   2x the merge cost. ZSTD(1) hot + `TTL ... RECOMPRESS CODEC(ZSTD(6))` at 13
   months captures the cold-tier gain off the write path.
4. **Typing is the free 31%.** v0→v1 with zero tuning. The naive dump also
   bloats every downstream cache and merge forever.

## Throughput (same box: 32 cores, lab server co-resident)

| path | rate |
|---|---|
| INSERT SELECT, in-server transform (storage ceiling) | 12.5M rows/s |
| JSONEachRow → `input()` transform → v8 table, 1 client | **1.39M events/s** (0.87 GB/s wire) |
| same, 8 parallel clients | **2.69M events/s** (1.69 GB/s wire) |
| query: daily uniq IPs, one path, 7d window | 27 ms (v7: 19 ms) |
| query: 3-step windowFunnel over all visitors | 45 ms (v7: 240 ms) |
| query: one visitor's full history | 20 ms (v7: 51 ms) |

JSON parsing is the ingest bottleneck (expected); even so, a single stream
sustains ~120B events/day against a current real volume of ~10^2/day. The
ingest tier will saturate on Connect handler/network long before ClickHouse.
The v8 ORDER BY is *faster* for the fraud/funnel archetypes (5.3x) and pays
~8ms on time-slice aggregates at this scale — the projection escape hatch is
recorded in the DDL if that inverts at real volume.

## Decision

**v8**: lean semantic schema, per-column codecs at ZSTD(1),
`ORDER BY (site, correlation_id, server_ts)`, monthly partitions, 13-month
ZSTD(6) recompress + 25-month delete TTL.

## Wire contract (reference-grounded, not freestyled)

The proto is the intersection of what Segment (track/common spec), PostHog
(capture API), Plausible (events API) and the OTel event/LogRecord model
agree on, checked against each in review:

- **Clients send**: event name, path, referrer, props, per-event `offset_ms`,
  batch-level `sent_at` — the minimal common set across all four references.
- **Server derives, no wire fields exist**: receipt time, IP, UA, geo, ASN,
  **trust tier**, and clock skew (`received_at − sent_at`, the
  Segment/PostHog pattern — clients never compute skew or send absolute
  times; Plausible proves you can run with server time alone).
- **Identity is the HMAC correlation cookie**, not a body field: unlike
  Segment's client-settable `anonymousId`, ours is server-minted and
  server-verified; a body-level id would just be a second, weaker channel.
- **Dedup/replay**: `(correlation_id, session_seq)` is a deterministic dedup
  key and gap detector — stronger than the references' random
  `messageId`/`insert_id` (which require a 1–7 day server-side ID store) and
  free: both columns already exist in storage.
- **Traces**: `trace_id` absent/all-zero = joins no server trace, exactly
  OTel logs.proto semantics; span granularity stays in the tracing backend.
  Web-vital fields mirror the OTel `browser.web_vital` semconv shape.
- The OTel ClickHouse exporter's own DDL (attribute maps + bloom-filter
  crutches + materialized hot columns) is the argument *for* dedicated typed
  columns when the schema is first-party and known: it materializes typed
  columns out of its maps for every hot key. We skip the map stage; if a
  long-tail property ever needs indexing, the escape hatch is one
  `Map(LowCardinality(String), String)` overflow column, not a redesign.

## Revisit triggers

- **Dictionary spill:** LowCardinality(correlation_id) assumes ≲1M distinct
  visitors per part. Watch `system.parts` dictionary sizes as volume grows;
  the fallback is plain FixedString(16) + ORDER BY unchanged (costs ~14B/event
  back).
- **UA cardinality:** if production UA distinct-count per part exceeds ~100k,
  demote `ua` to `String CODEC(ZSTD(1))`.
- **Time-slice queries at scale:** if month-partition scans for per-path
  aggregates exceed budget, add the (site, path, day) projection rather than
  reordering the base table.
- **Props growth:** `props` is a capped JSON string today (0.6 B/event). If it
  becomes load-bearing, evaluate the native JSON type then — not before.

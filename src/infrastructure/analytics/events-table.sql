-- Analytics/fraud event store — bake-off winner, 17.0 B/event, 37x under
-- the JSON wire form (evidence: docs/analytics-storage-design.md; wire
-- contract: src/proto/guardian/analytics/v1/events.proto). Visitor-clustered
-- ORDER BY is what buys both the compression and the fraud/funnel query
-- shape; time-sliced aggregates get a projection later if ever needed.
CREATE TABLE IF NOT EXISTS guardian_analytics.events
(
    server_ts      DateTime64(3) CODEC(Delta(8), ZSTD(1)),
    site           LowCardinality(String) CODEC(ZSTD(1)),
    event_name     LowCardinality(String) CODEC(ZSTD(1)),
    trust_tier     Enum8('server_observed' = 1, 'edge_verified' = 2, 'client_claimed' = 3) CODEC(ZSTD(1)),
    schema_version UInt8 CODEC(ZSTD(1)),

    -- All-zero unless the event joins a server trace (sparse: 16 -> ~2 B/event).
    trace_id       FixedString(16) CODEC(ZSTD(1)),

    -- HMAC correlation cookie. LowCardinality is deliberate: the per-part
    -- dictionary turns 16 random bytes into ~1.6 B/event (revisit >1M/part).
    correlation_id LowCardinality(FixedString(16)) CODEC(ZSTD(1)),
    session_seq    UInt16 CODEC(T64, ZSTD(1)),

    path           LowCardinality(String) CODEC(ZSTD(1)),
    referrer       LowCardinality(String) CODEC(ZSTD(1)),
    ua             LowCardinality(String) CODEC(ZSTD(1)),

    client_ip      IPv6 CODEC(ZSTD(1)),
    ip_source      LowCardinality(String) CODEC(ZSTD(1)),
    country        LowCardinality(String) CODEC(ZSTD(1)),
    asn            UInt32 CODEC(T64, ZSTD(1)),

    status         UInt16 CODEC(T64, ZSTD(1)),
    duration_ms    UInt32 CODEC(T64, ZSTD(1)),
    -- Server-derived: received_at - batch sent_at (never client-computed).
    client_skew_ms Int32 CODEC(T64, ZSTD(1)),

    vital_name     LowCardinality(String) CODEC(ZSTD(1)),
    vital_value    Float64 CODEC(Gorilla, ZSTD(1)),
    props          String CODEC(ZSTD(1))
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(server_ts)
ORDER BY (site, correlation_id, server_ts)
TTL toDateTime(server_ts) + INTERVAL 13 MONTH RECOMPRESS CODEC(ZSTD(6)),
    toDateTime(server_ts) + INTERVAL 25 MONTH DELETE;

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
    -- 'unspecified' = 0 so an insert that omits the column cannot silently
    -- claim the highest-trust tier (Enum8 defaults to 0).
    trust_tier     Enum8('unspecified' = 0, 'server_observed' = 1, 'edge_verified' = 2, 'client_claimed' = 3) CODEC(ZSTD(1)),
    schema_version UInt8 CODEC(ZSTD(1)),

    -- All-zero unless the event joins a server trace (~11% of events;
    -- sparse: 16 -> ~2 B/event). Ingest must length-validate: FixedString
    -- zero-pads short values silently and aborts the block on long ones.
    trace_id       FixedString(16) CODEC(ZSTD(1)),

    -- HMAC correlation cookie. Plain FixedString, not LowCardinality: the
    -- ORDER BY clusters each visitor into runs, which ZSTD compresses better
    -- than a dictionary (1.37 vs 1.61 B/event, A/B in the design doc) with
    -- no dictionary-spill regime to manage.
    correlation_id FixedString(16) CODEC(ZSTD(1)),
    session_seq    UInt32 CODEC(T64, ZSTD(1)),

    path           LowCardinality(String) CODEC(ZSTD(1)),
    referrer       LowCardinality(String) CODEC(ZSTD(1)),
    ua             LowCardinality(String) CODEC(ZSTD(1)),
    -- Parsed from ua at ingest; the raw string stays for re-derivation.
    device_class   LowCardinality(String) CODEC(ZSTD(1)),
    os_family      LowCardinality(String) CODEC(ZSTD(1)),
    browser_family LowCardinality(String) CODEC(ZSTD(1)),

    -- Raw IP is abuse forensics only: the column TTL zeroes it at 90 days
    -- while the derived country/asn/device fields live the row's full 25
    -- months. Derivations happen at ingest, so nothing dies with the IP.
    client_ip      IPv6 CODEC(ZSTD(1)) TTL toDateTime(server_ts) + INTERVAL 90 DAY,
    ip_source      LowCardinality(String) CODEC(ZSTD(1)),
    -- CF-IPCountry via the verify-gated ingress map (edge-observed);
    -- asn resolved at ingest from the image-baked BGP snapshot (fresh as
    -- of the last snapshot refresh, frozen on the row thereafter).
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
-- No RECOMPRESS TTL: it skips columns with explicit codecs (verified live —
-- a scheduled full-part rewrite with zero compression change), and full
-- ZSTD(6) measured only ~2.7% smaller anyway.
TTL toDateTime(server_ts) + INTERVAL 25 MONTH DELETE;

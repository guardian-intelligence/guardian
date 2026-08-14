-- Single-node form of the otel_traces DDL in
-- src/infrastructure/deployments/analytics/system/traces-configmap.yaml
-- (Replicated engines and ON CLUSTER stripped; columns, codecs, indexes,
-- partitioning, ORDER BY, TTL, and the MV are identical).
CREATE DATABASE IF NOT EXISTS guardian_analytics;

CREATE TABLE IF NOT EXISTS guardian_analytics.otel_traces
(
    Timestamp          DateTime64(9) CODEC(Delta(8), ZSTD(1)),
    TraceId            String CODEC(ZSTD(1)),
    SpanId             String CODEC(ZSTD(1)),
    ParentSpanId       String CODEC(ZSTD(1)),
    TraceState         String CODEC(ZSTD(1)),
    SpanName           LowCardinality(String) CODEC(ZSTD(1)),
    SpanKind           LowCardinality(String) CODEC(ZSTD(1)),
    ServiceName        LowCardinality(String) CODEC(ZSTD(1)),
    ResourceAttributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    ScopeName          String CODEC(ZSTD(1)),
    ScopeVersion       String CODEC(ZSTD(1)),
    SpanAttributes     Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    Duration           UInt64 CODEC(T64, ZSTD(1)),
    StatusCode         LowCardinality(String) CODEC(ZSTD(1)),
    StatusMessage      String CODEC(ZSTD(1)),
    Events Nested (
        Timestamp  DateTime64(9),
        Name       LowCardinality(String),
        Attributes Map(LowCardinality(String), String)
    ) CODEC(ZSTD(1)),
    Links Nested (
        TraceId    String,
        SpanId     String,
        TraceState String,
        Attributes Map(LowCardinality(String), String)
    ) CODEC(ZSTD(1)),
    INDEX idx_trace_id TraceId TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_duration Duration TYPE minmax GRANULARITY 1
)
ENGINE = MergeTree
PARTITION BY toDate(Timestamp)
ORDER BY (ServiceName, toStartOfHour(Timestamp), TraceId, Timestamp)
TTL toDateTime(Timestamp) + INTERVAL 6 MONTH DELETE
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

-- trace_id -> [minTs, maxTs] lookup: given a trace_id from a log line,
-- bound the time window before scanning the main table.
CREATE TABLE IF NOT EXISTS guardian_analytics.otel_traces_trace_id_ts
(
    TraceId String CODEC(ZSTD(1)),
    Start   DateTime64(9) CODEC(Delta(8), ZSTD(1)),
    End     DateTime64(9) CODEC(Delta(8), ZSTD(1)),
    INDEX idx_trace_id TraceId TYPE bloom_filter(0.001) GRANULARITY 1
)
ENGINE = AggregatingMergeTree
PARTITION BY toDate(Start)
ORDER BY (TraceId, Start)
TTL toDateTime(Start) + INTERVAL 6 MONTH DELETE
SETTINGS ttl_only_drop_parts = 1;

CREATE MATERIALIZED VIEW IF NOT EXISTS guardian_analytics.otel_traces_trace_id_ts_mv
TO guardian_analytics.otel_traces_trace_id_ts AS
SELECT
    TraceId,
    min(Timestamp) AS Start,
    max(Timestamp) AS End
FROM guardian_analytics.otel_traces
WHERE TraceId != ''
GROUP BY TraceId;

#!/usr/bin/env bash
# Schema bake-off: create each variant, timed 20M-row INSERT SELECT from the
# wire-truth source, OPTIMIZE FINAL, then read settled sizes from system.parts.
set -euo pipefail
CH="clickhouse client --port 9111"
OUT="$(dirname "$0")/results_variants.tsv"
echo -e "variant\tinsert_s\trows_per_s\toptimize_s\tcompressed\tuncompressed\tratio\tbytes_per_event" > "$OUT"

# Typed SELECT shared by v1..v6 (casts from wire-truth source types).
TYPED_SELECT="SELECT server_ts, site, event_name,
  CAST(trust_tier AS Enum8('server_observed'=1,'edge_verified'=2,'client_claimed'=3)) AS trust_tier,
  schema_version, trace_id, span_id, correlation_id, session_seq, path, referrer, ua,
  toIPv6(client_ip) AS client_ip, ip_source, country, asn, status, duration_ms,
  client_ts, vital_name, vital_value, props
FROM lab.events_source"

NAIVE_SELECT="SELECT * FROM lab.events_source"

declare -A DDL SEL

DDL[v0_naive]="CREATE TABLE lab.v0_naive AS lab.events_source ENGINE=MergeTree ORDER BY server_ts"
SEL[v0_naive]="$NAIVE_SELECT"

TYPED_COLS_PLAIN="server_ts DateTime64(3), site LowCardinality(String), event_name LowCardinality(String),
 trust_tier Enum8('server_observed'=1,'edge_verified'=2,'client_claimed'=3), schema_version UInt8,
 trace_id FixedString(16), span_id FixedString(8), correlation_id UUID, session_seq UInt16,
 path LowCardinality(String), referrer LowCardinality(String), ua LowCardinality(String),
 client_ip IPv6, ip_source LowCardinality(String), country LowCardinality(String), asn UInt32,
 status UInt16, duration_ms UInt32, client_ts DateTime64(3), vital_name LowCardinality(String),
 vital_value Float64, props String"

codec_cols () { # $1 = zstd level for general strings, $2 = zstd level inside delta chains
  echo "server_ts DateTime64(3) CODEC(Delta(8), ZSTD($2)), site LowCardinality(String) CODEC(ZSTD($1)),
 event_name LowCardinality(String) CODEC(ZSTD($1)),
 trust_tier Enum8('server_observed'=1,'edge_verified'=2,'client_claimed'=3) CODEC(ZSTD($1)),
 schema_version UInt8 CODEC(ZSTD($1)), trace_id FixedString(16) CODEC(NONE), span_id FixedString(8) CODEC(NONE),
 correlation_id UUID CODEC(ZSTD($1)), session_seq UInt16 CODEC(T64, ZSTD($1)),
 path LowCardinality(String) CODEC(ZSTD($1)), referrer LowCardinality(String) CODEC(ZSTD($1)),
 ua LowCardinality(String) CODEC(ZSTD($1)), client_ip IPv6 CODEC(ZSTD($1)),
 ip_source LowCardinality(String) CODEC(ZSTD($1)), country LowCardinality(String) CODEC(ZSTD($1)),
 asn UInt32 CODEC(T64, ZSTD($1)), status UInt16 CODEC(T64, ZSTD($1)), duration_ms UInt32 CODEC(T64, ZSTD($1)),
 client_ts DateTime64(3) CODEC(Delta(8), ZSTD($2)), vital_name LowCardinality(String) CODEC(ZSTD($1)),
 vital_value Float64 CODEC(Gorilla, ZSTD($1)), props String CODEC(ZSTD($1))"
}

DDL[v1_typed]="CREATE TABLE lab.v1_typed ($TYPED_COLS_PLAIN) ENGINE=MergeTree
 PARTITION BY toYYYYMM(server_ts) ORDER BY (site, event_name, server_ts)"
DDL[v2_codec]="CREATE TABLE lab.v2_codec ($(codec_cols 1 1)) ENGINE=MergeTree
 PARTITION BY toYYYYMM(server_ts) ORDER BY (site, event_name, server_ts)"
DDL[v3_deep_order]="CREATE TABLE lab.v3_deep_order ($(codec_cols 1 1)) ENGINE=MergeTree
 PARTITION BY toYYYYMM(server_ts) ORDER BY (site, event_name, path, correlation_id, server_ts)"
DDL[v4_ts_order]="CREATE TABLE lab.v4_ts_order ($(codec_cols 1 1)) ENGINE=MergeTree
 PARTITION BY toYYYYMM(server_ts) ORDER BY (server_ts)"
DDL[v5_zstd3]="CREATE TABLE lab.v5_zstd3 ($(codec_cols 3 3)) ENGINE=MergeTree
 PARTITION BY toYYYYMM(server_ts) ORDER BY (site, event_name, server_ts)"
DDL[v6_cold6]="CREATE TABLE lab.v6_cold6 ($(codec_cols 6 6)) ENGINE=MergeTree
 PARTITION BY toYYYYMM(server_ts) ORDER BY (site, event_name, server_ts)"
for v in v1_typed v2_codec v3_deep_order v4_ts_order v5_zstd3 v6_cold6; do SEL[$v]="$TYPED_SELECT"; done

for v in v0_naive v1_typed v2_codec v3_deep_order v4_ts_order v5_zstd3 v6_cold6; do
  $CH -q "DROP TABLE IF EXISTS lab.$v"
  $CH -q "${DDL[$v]}"
  t0=$(date +%s.%N)
  $CH -q "INSERT INTO lab.$v ${SEL[$v]} SETTINGS max_insert_threads=8, max_threads=16"
  t1=$(date +%s.%N)
  $CH -q "OPTIMIZE TABLE lab.$v FINAL"
  t2=$(date +%s.%N)
  ins=$(echo "$t1 $t0" | awk '{printf "%.1f", $1-$2}')
  opt=$(echo "$t2 $t1" | awk '{printf "%.1f", $2>0?$1-$2:0}' 2>/dev/null || echo 0)
  opt=$(echo "$t2 $t1" | awk '{printf "%.1f", $1-$2}')
  rps=$(echo "$ins" | awk '{printf "%.0f", 20000000/$1}')
  read -r comp uncomp ratio bpe <<< "$($CH -q "
    SELECT sum(data_compressed_bytes), sum(data_uncompressed_bytes),
           round(sum(data_uncompressed_bytes)/sum(data_compressed_bytes),2),
           round(sum(data_compressed_bytes)/20000000,1)
    FROM system.parts WHERE database='lab' AND table='$v' AND active FORMAT TSV" | tr '\t' ' ')"
  echo -e "$v\t$ins\t$rps\t$opt\t$comp\t$uncomp\t$ratio\t$bpe" | tee -a "$OUT"
done

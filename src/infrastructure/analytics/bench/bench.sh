#!/usr/bin/env bash
# Schema bake-off harness
# (docs/adrs/0002-analytics-event-storage-and-wire-contract.md). Regenerates
# every decision-critical number: variant sizes v0-v9 + the shipping DDL,
# JSONEachRow ingest throughput, and the three query archetypes.
# Usage: bench.sh <clickhouse-port> [repo-root]   (needs lab.events_source
# from gen.sql; shipping DDL read from src/infrastructure/analytics/).
set -euo pipefail
PORT="${1:?clickhouse port}"
ROOT="${2:-$(git rev-parse --show-toplevel)}"
CH="clickhouse client --port $PORT"
OUT="$(dirname "$0")/results_variants.tsv"
echo -e "variant\tinsert_s\trows_per_s\tcompressed\tuncompressed\tratio\tbytes_per_event" > "$OUT"

TYPED_SELECT="SELECT server_ts, site, event_name,
  CAST(trust_tier AS Enum8('server_observed'=1,'edge_verified'=2,'client_claimed'=3)) AS trust_tier,
  schema_version, trace_id, span_id, correlation_id, session_seq, path, referrer, ua,
  toIPv6(client_ip) AS client_ip, ip_source, country, asn, status, duration_ms,
  client_ts, vital_name, vital_value, props
FROM lab.events_source"

LEAN_SELECT="SELECT server_ts, site, event_name,
  CAST(trust_tier AS Enum8('unspecified'=0,'server_observed'=1,'edge_verified'=2,'client_claimed'=3)) AS trust_tier,
  schema_version,
  if(event_name IN ('rpc','error'), trace_id, toFixedString(unhex(repeat('00',16)),16)) AS trace_id,
  UUIDStringToNum(toString(correlation_id)) AS correlation_id,
  toUInt32(session_seq) AS session_seq, path, referrer, ua, toIPv6(client_ip) AS client_ip,
  ip_source, country, asn, status, duration_ms,
  if(client_ts = toDateTime64(0,3), -1,
     toInt32(toUnixTimestamp64Milli(server_ts) - toUnixTimestamp64Milli(client_ts))) AS client_skew_ms,
  vital_name, vital_value, props
FROM lab.events_source"

TYPED_COLS_PLAIN="server_ts DateTime64(3), site LowCardinality(String), event_name LowCardinality(String),
 trust_tier Enum8('server_observed'=1,'edge_verified'=2,'client_claimed'=3), schema_version UInt8,
 trace_id FixedString(16), span_id FixedString(8), correlation_id UUID, session_seq UInt16,
 path LowCardinality(String), referrer LowCardinality(String), ua LowCardinality(String),
 client_ip IPv6, ip_source LowCardinality(String), country LowCardinality(String), asn UInt32,
 status UInt16, duration_ms UInt32, client_ts DateTime64(3), vital_name LowCardinality(String),
 vital_value Float64, props String"

codec_cols () { # $1 = general zstd level, $2 = delta-chain zstd level
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

lean_cols () { # $1 = correlation_id type
  echo "server_ts DateTime64(3) CODEC(Delta(8), ZSTD(1)), site LowCardinality(String) CODEC(ZSTD(1)),
 event_name LowCardinality(String) CODEC(ZSTD(1)),
 trust_tier Enum8('unspecified'=0,'server_observed'=1,'edge_verified'=2,'client_claimed'=3) CODEC(ZSTD(1)),
 schema_version UInt8 CODEC(ZSTD(1)), trace_id FixedString(16) CODEC(ZSTD(1)),
 correlation_id $1 CODEC(ZSTD(1)), session_seq UInt32 CODEC(T64, ZSTD(1)),
 path LowCardinality(String) CODEC(ZSTD(1)), referrer LowCardinality(String) CODEC(ZSTD(1)),
 ua LowCardinality(String) CODEC(ZSTD(1)), client_ip IPv6 CODEC(ZSTD(1)),
 ip_source LowCardinality(String) CODEC(ZSTD(1)), country LowCardinality(String) CODEC(ZSTD(1)),
 asn UInt32 CODEC(T64, ZSTD(1)), status UInt16 CODEC(T64, ZSTD(1)), duration_ms UInt32 CODEC(T64, ZSTD(1)),
 client_skew_ms Int32 CODEC(T64, ZSTD(1)), vital_name LowCardinality(String) CODEC(ZSTD(1)),
 vital_value Float64 CODEC(Gorilla, ZSTD(1)), props String CODEC(ZSTD(1))"
}

declare -A DDL SEL
DDL[v0_naive]="CREATE TABLE lab.v0_naive AS lab.events_source ENGINE=MergeTree ORDER BY server_ts"
SEL[v0_naive]="SELECT * FROM lab.events_source"
DDL[v1_typed]="CREATE TABLE lab.v1_typed ($TYPED_COLS_PLAIN) ENGINE=MergeTree PARTITION BY toYYYYMM(server_ts) ORDER BY (site, event_name, server_ts)"
DDL[v2_codec]="CREATE TABLE lab.v2_codec ($(codec_cols 1 1)) ENGINE=MergeTree PARTITION BY toYYYYMM(server_ts) ORDER BY (site, event_name, server_ts)"
DDL[v3_deep_order]="CREATE TABLE lab.v3_deep_order ($(codec_cols 1 1)) ENGINE=MergeTree PARTITION BY toYYYYMM(server_ts) ORDER BY (site, event_name, path, correlation_id, server_ts)"
DDL[v4_ts_order]="CREATE TABLE lab.v4_ts_order ($(codec_cols 1 1)) ENGINE=MergeTree PARTITION BY toYYYYMM(server_ts) ORDER BY (server_ts)"
DDL[v5_zstd3]="CREATE TABLE lab.v5_zstd3 ($(codec_cols 3 3)) ENGINE=MergeTree PARTITION BY toYYYYMM(server_ts) ORDER BY (site, event_name, server_ts)"
DDL[v6_cold6]="CREATE TABLE lab.v6_cold6 ($(codec_cols 6 6)) ENGINE=MergeTree PARTITION BY toYYYYMM(server_ts) ORDER BY (site, event_name, server_ts)"
for v in v1_typed v2_codec v3_deep_order v4_ts_order v5_zstd3 v6_cold6; do SEL[$v]="$TYPED_SELECT"; done
DDL[v7_lean]="CREATE TABLE lab.v7_lean ($(lean_cols 'LowCardinality(FixedString(16))')) ENGINE=MergeTree PARTITION BY toYYYYMM(server_ts) ORDER BY (site, event_name, server_ts)"
DDL[v8_lc_corr]="CREATE TABLE lab.v8_lc_corr ($(lean_cols 'LowCardinality(FixedString(16))')) ENGINE=MergeTree PARTITION BY toYYYYMM(server_ts) ORDER BY (site, correlation_id, server_ts)"
DDL[v9_lean_day_corr]="CREATE TABLE lab.v9_lean_day_corr ($(lean_cols 'LowCardinality(FixedString(16))')) ENGINE=MergeTree PARTITION BY toYYYYMM(server_ts) ORDER BY (site, toDate(server_ts), correlation_id, server_ts)"
for v in v7_lean v8_lc_corr v9_lean_day_corr; do SEL[$v]="$LEAN_SELECT"; done

measure () { # $1 db.table  $2 label  $3 insert_s
  read -r comp uncomp ratio bpe <<< "$($CH -q "
    SELECT sum(data_compressed_bytes), sum(data_uncompressed_bytes),
           round(sum(data_uncompressed_bytes)/sum(data_compressed_bytes),2),
           round(sum(data_compressed_bytes)/20000000,2)
    FROM system.parts WHERE database||'.'||table='$1' AND active FORMAT TSV" | tr '\t' ' ')"
  rps=$(echo "$3" | awk '{printf "%.0f", 20000000/$1}')
  echo -e "$2\t$3\t$rps\t$comp\t$uncomp\t$ratio\t$bpe" | tee -a "$OUT"
}

for v in v0_naive v1_typed v2_codec v3_deep_order v4_ts_order v5_zstd3 v6_cold6 v7_lean v8_lc_corr v9_lean_day_corr; do
  $CH -q "DROP TABLE IF EXISTS lab.$v"
  $CH -q "${DDL[$v]}"
  t0=$(date +%s.%N)
  $CH -q "INSERT INTO lab.$v ${SEL[$v]} SETTINGS max_insert_threads=8, max_threads=16"
  t1=$(date +%s.%N)
  $CH -q "OPTIMIZE TABLE lab.$v FINAL"
  measure "lab.$v" "$v" "$(echo "$t1 $t0" | awk '{printf "%.1f", $1-$2}')"
done

# The shipping DDL (plain FixedString correlation_id, no RECOMPRESS TTL).
$CH -q "CREATE DATABASE IF NOT EXISTS guardian_analytics"
$CH -q "DROP TABLE IF EXISTS guardian_analytics.events"
$CH --multiquery < "$ROOT/src/infrastructure/analytics/events-table.sql"
t0=$(date +%s.%N)
$CH -q "INSERT INTO guardian_analytics.events $LEAN_SELECT SETTINGS max_insert_threads=8, max_threads=16"
t1=$(date +%s.%N)
$CH -q "OPTIMIZE TABLE guardian_analytics.events FINAL"
measure "guardian_analytics.events" "shipping_ddl" "$(echo "$t1 $t0" | awk '{printf "%.1f", $1-$2}')"

# Ingest throughput through the wire form (JSONEachRow -> input() transform).
WIRE=$(dirname "$0")/wire.jsonl
$CH -q "SELECT server_ts, site, event_name, trust_tier, schema_version, hex(trace_id) AS trace_id,
  hex(span_id) AS span_id, correlation_id, session_seq, path, referrer, ua, client_ip, ip_source,
  country, asn, status, duration_ms, client_ts, vital_name, vital_value, props
FROM lab.events_source LIMIT 5000000 FORMAT JSONEachRow" > "$WIRE"
WIRE_B=$(stat -c%s "$WIRE")
IN="server_ts DateTime64(3), site String, event_name String, trust_tier UInt8, schema_version UInt8, trace_id String, span_id String, correlation_id UUID, session_seq UInt16, path String, referrer String, ua String, client_ip String, ip_source String, country String, asn UInt32, status UInt16, duration_ms UInt32, client_ts DateTime64(3), vital_name String, vital_value Float64, props String"
ISEL="SELECT server_ts, site, event_name, CAST(trust_tier AS Enum8('unspecified'=0,'server_observed'=1,'edge_verified'=2,'client_claimed'=3)), schema_version,
 if(event_name IN ('rpc','error'), toFixedString(unhex(trace_id),16), toFixedString(unhex(repeat('00',16)),16)),
 UUIDStringToNum(toString(correlation_id)), toUInt32(session_seq), path, referrer, ua, toIPv6(client_ip), ip_source, country, asn, status, duration_ms,
 if(client_ts = toDateTime64(0,3), -1, toInt32(toUnixTimestamp64Milli(server_ts) - toUnixTimestamp64Milli(client_ts))),
 vital_name, vital_value, props FROM input('$IN')"
$CH -q "DROP TABLE IF EXISTS lab.ingest_test"; $CH -q "CREATE TABLE lab.ingest_test AS guardian_analytics.events"
t0=$(date +%s.%N); $CH -q "INSERT INTO lab.ingest_test $ISEL FORMAT JSONEachRow" < "$WIRE"; t1=$(date +%s.%N)
echo -e "ingest_json_1client\t$(echo "$t1 $t0" | awk '{printf "%.1f", $1-$2}')\t$(echo "$t1 $t0" | awk '{printf "%.0f", 5000000/($1-$2)}')\t$WIRE_B\t\t\t$(echo "$WIRE_B" | awk '{printf "%.1f", $1/5000000}')" | tee -a "$OUT"
$CH -q "TRUNCATE TABLE lab.ingest_test"
split -n l/8 --numeric-suffixes=1 "$WIRE" "$(dirname "$0")/chunk_"
t0=$(date +%s.%N); for i in 1 2 3 4 5 6 7 8; do $CH -q "INSERT INTO lab.ingest_test $ISEL FORMAT JSONEachRow" < "$(dirname "$0")/chunk_0$i" & done; wait; t1=$(date +%s.%N)
echo -e "ingest_json_8clients\t$(echo "$t1 $t0" | awk '{printf "%.1f", $1-$2}')\t$(echo "$t1 $t0" | awk '{printf "%.0f", 5000000/($1-$2)}')" | tee -a "$OUT"
rm -f "$(dirname "$0")"/chunk_0? "$WIRE"

# Query archetypes on the LC control vs the shipping table.
for t in lab.v8_lc_corr guardian_analytics.events; do
  for q in "timeslice:SELECT toDate(server_ts) d, uniqExact(client_ip) FROM $t WHERE site='prod' AND path='/letters/dear-shovon' AND server_ts >= '2026-06-26' GROUP BY d ORDER BY d" \
           "funnel:SELECT countIf(lvl>=3) FROM (SELECT correlation_id, windowFunnel(3600)(toDateTime(server_ts), path='/', path='/pricing', path='/signup') lvl FROM $t WHERE site='prod' GROUP BY correlation_id)" \
           "visitor_history:SELECT * FROM $t WHERE site='prod' AND correlation_id = (SELECT any(correlation_id) FROM $t) ORDER BY server_ts"; do
    name="${q%%:*}"; sql="${q#*:}"
    el=$( $CH --time -q "$sql FORMAT Null" 2>&1 | tail -1 )
    echo -e "query_${name}_${t}\t$el" | tee -a "$OUT"
  done
done

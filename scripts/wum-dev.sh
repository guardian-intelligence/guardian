#!/usr/bin/env bash
# Wake Up Mythra local development stack, fronted by `aspect mythra dev`:
#
#   scripts/wum-dev.sh [up|down|status|logs [leg]|smoke|latency]
#
# Everything mirrors prod: same admission path, same module distribution,
# same ingress split.
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$PWD"
APP="$ROOT/src/games/wake-up-mythra/web"
RUN_DIR="$ROOT/.guardian/dev/wum"
ISSUER_PORT="${WUM_DEV_ISSUER_PORT:-9635}"
PG_PORT="${WUM_DEV_PG_PORT:-55432}"
CH_PORT="${WUM_DEV_CH_PORT:-59000}"
CH_HTTP_PORT="${WUM_DEV_CH_HTTP_PORT:-58123}"
INGEST_PORT="${WUM_DEV_INGEST_PORT:-9636}"
ISSUER="http://127.0.0.1:${ISSUER_PORT}/realms/dev"
GATEWAY_HTTP_PORT="${WUM_DEV_GATEWAY_HTTP_PORT:-9634}"
GATEWAY_METRICS_PORT="${WUM_DEV_GATEWAY_METRICS_PORT:-9633}"
GATEWAY_WT_PORT="${WUM_DEV_GATEWAY_WT_PORT:-4433}"
PARK_PORT="${WUM_DEV_PARK_PORT:-9632}"
PARK_HTTP_PORT="${WUM_DEV_PARK_HTTP_PORT:-9631}"
PARK_METRICS_PORT="${WUM_DEV_PARK_METRICS_PORT:-9637}"
WEB_PORT=4254
OTLP_GRPC_PORT="${WUM_DEV_OTLP_GRPC_PORT:-4317}"
OTLP_HTTP_PORT="${WUM_DEV_OTLP_HTTP_PORT:-4318}"
FLAGD_PORT="${WUM_DEV_FLAGD_PORT:-8013}"
FLAGD_MGMT_PORT="${WUM_DEV_FLAGD_MGMT_PORT:-8014}"
# The web app's dev flag client hardcodes OFREP at 127.0.0.1:8016.
FLAGD_OFREP_PORT=8016
FLAGS_FILE="$ROOT/src/infrastructure/deployments/flags/prod/flags/flags.json"
DEV_TICK_HZ="${WUM_DEV_TICK_HZ:-24}"

LEGS="pg ch otelcol flagd ingest devissuer park gateway web"
STARTED=""

pidfile() { echo "$RUN_DIR/$1.pid"; }
logfile() { echo "$RUN_DIR/$1.log"; }

leg_pid() {
  cat "$(pidfile "$1")" 2>/dev/null || true
}

leg_command_pattern() {
  case "$1" in
  pg) echo "postgres" ;;
  ch) echo "clickhouse" ;;
  otelcol) echo "otelcol-contrib" ;;
  flagd) echo "flagd" ;;
  ingest) echo "analytics/ingest" ;;
  devissuer) echo "devissuer" ;;
  park) echo "chunkies-chunkie" ;;
  gateway) echo "chunkies-gateway" ;;
  web) echo "vp dev" ;;
  esac
}

leg_alive() {
  pid="$(leg_pid "$1")"
  [ -n "$pid" ] || return 1
  case "$(ps -p "$pid" -o args= 2>/dev/null)" in
  *"$(leg_command_pattern "$1")"*) return 0 ;;
  esac
  rm -f "$(pidfile "$1")"
  return 1
}

leg_port() {
  case "$1" in
  pg) echo "$PG_PORT" ;;
  ch) echo "$CH_PORT" ;;
  otelcol) echo "$OTLP_GRPC_PORT" ;;
  flagd) echo "$FLAGD_OFREP_PORT" ;;
  ingest) echo "$INGEST_PORT" ;;
  devissuer) echo "$ISSUER_PORT" ;;
  park) echo "$PARK_PORT" ;;
  gateway) echo "$GATEWAY_HTTP_PORT" ;;
  web) echo "$WEB_PORT" ;;
  esac
}

probe() {
  case "$1" in
  pg) [ "$(leg_pid pg)" = "$(head -1 "$RUN_DIR/pgdata/postmaster.pid" 2>/dev/null)" ] && (exec 3<>"/dev/tcp/127.0.0.1/$PG_PORT") 2>/dev/null ;;
  ch) [ "$(curl -fsS --max-time 2 "http://127.0.0.1:${CH_HTTP_PORT}/?query=SELECT+1" 2>/dev/null)" = 1 ] ;;
  # Ownership, not reachability: a foreign process already on the OTLP port
  # would answer a bare connect while our collector dies on bind.
  otelcol) lsof -nP -a -p "$(leg_pid otelcol)" -iTCP:"$OTLP_GRPC_PORT" -sTCP:LISTEN >/dev/null 2>&1 ;;
  flagd) curl -fsS --max-time 2 "http://127.0.0.1:${FLAGD_MGMT_PORT}/readyz" >/dev/null 2>&1 ;;
  ingest) curl -fsS --max-time 2 "http://127.0.0.1:${INGEST_PORT}/healthz" >/dev/null 2>&1 ;;
  devissuer) curl -fsS --max-time 2 "$ISSUER/.well-known/openid-configuration" >/dev/null 2>&1 ;;
  park) curl -fsS --max-time 2 "http://127.0.0.1:${PARK_METRICS_PORT}/readyz" >/dev/null 2>&1 ;;
  gateway) curl -fsS --max-time 2 "http://127.0.0.1:${GATEWAY_METRICS_PORT}/readyz" >/dev/null 2>&1 ;;
  web) [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://127.0.0.1:${WEB_PORT}/" 2>/dev/null)" = 200 ] ;;
  esac
}

descendants() {
  local child
  for child in $(pgrep -P "$1" 2>/dev/null); do
    descendants "$child"
    echo "$child"
  done
}

stop_leg() {
  leg="$1"
  if [ "$leg" = pg ]; then
    scripts/wum-dev-db.sh stop || return 1
    rm -f "$(pidfile pg)"
    return 0
  fi
  pid="$(leg_pid "$leg")"
  if leg_alive "$leg"; then
    victims="$(descendants "$pid") $pid"
    kill $victims 2>/dev/null || true
    for _ in $(seq 1 20); do
      still=""
      for v in $victims; do
        kill -0 "$v" 2>/dev/null && still="$still $v"
      done
      [ -z "$still" ] && break
      sleep 0.25
    done
    for v in $victims; do
      kill -0 "$v" 2>/dev/null && kill -9 "$v" 2>/dev/null || true
    done
  fi
  rm -f "$(pidfile "$leg")"
}

fail_leg() {
  leg="$1"
  echo "wum-dev: $leg failed its readiness gate; last log lines:" >&2
  tail -n 20 "$(logfile "$leg")" >&2 || true
  for started in $STARTED; do
    stop_leg "$started" || true
  done
  exit 1
}

acquire_lock() {
  mkdir -p "$RUN_DIR"
  if ! mkdir "$RUN_DIR/lock" 2>/dev/null; then
    echo "wum-dev: another up/down is in flight (if stale: rmdir $RUN_DIR/lock)" >&2
    exit 1
  fi
  trap 'rmdir "$RUN_DIR/lock" 2>/dev/null' EXIT
}

await_ready() {
  leg="$1"
  deadline_seconds="$2"
  ticks=0
  while [ "$ticks" -lt $((deadline_seconds * 2)) ]; do
    leg_alive "$leg" || return 1
    probe "$leg" && return 0
    sleep 0.5
    ticks=$((ticks + 1))
  done
  return 1
}

start_leg() {
  leg="$1"
  if leg_alive "$leg"; then
    echo "wum-dev: $leg already running (pid $(leg_pid "$leg"))" >&2
    return 0
  fi
  echo "wum-dev: starting ${leg}…" >&2
  case "$leg" in
  pg)
    STARTED="pg $STARTED"
    scripts/wum-dev-db.sh start || fail_leg pg
    ;;
  ch)
    STARTED="ch $STARTED"
    mkdir -p "$RUN_DIR/ch/data"
    cat >"$RUN_DIR/ch/config.xml" <<EOF
<clickhouse>
  <path>$RUN_DIR/ch/data/</path>
  <listen_host>127.0.0.1</listen_host>
  <tcp_port>$CH_PORT</tcp_port>
  <http_port>$CH_HTTP_PORT</http_port>
  <logger>
    <console>1</console>
    <level>warning</level>
  </logger>
  <user_directories>
    <users_xml>
      <path>config.xml</path>
    </users_xml>
  </user_directories>
  <users>
    <default>
      <password></password>
      <networks><ip>127.0.0.1</ip><ip>::1</ip></networks>
      <profile>default</profile>
      <quota>default</quota>
      <access_management>1</access_management>
    </default>
  </users>
  <profiles><default/></profiles>
  <quotas><default/></quotas>
</clickhouse>
EOF
    "$ROOT/$CLICKHOUSE" server --config-file="$RUN_DIR/ch/config.xml" >>"$(logfile ch)" 2>&1 &
    echo $! >"$(pidfile ch)"
    await_ready ch 60 || fail_leg ch
    ;;
  otelcol)
    STARTED="otelcol $STARTED"
    WUM_DEV_CH_PORT="$CH_PORT" \
      WUM_DEV_OTLP_GRPC_PORT="$OTLP_GRPC_PORT" \
      WUM_DEV_OTLP_HTTP_PORT="$OTLP_HTTP_PORT" \
      "$ROOT/$OTELCOL" --config="$ROOT/scripts/wum-dev-otelcol.yaml" >>"$(logfile otelcol)" 2>&1 &
    echo $! >"$(pidfile otelcol)"
    await_ready otelcol 60 || fail_leg otelcol
    ;;
  flagd)
    STARTED="flagd $STARTED"
    "$ROOT/$FLAGD" start \
      --sources "[{\"uri\":\"$FLAGS_FILE\",\"provider\":\"fsnotify\"}]" \
      --port="$FLAGD_PORT" --management-port="$FLAGD_MGMT_PORT" --ofrep-port="$FLAGD_OFREP_PORT" \
      --cors-origin='*' --log-format=json >>"$(logfile flagd)" 2>&1 &
    echo $! >"$(pidfile flagd)"
    await_ready flagd 60 || fail_leg flagd
    ;;
  ingest)
    STARTED="ingest $STARTED"
    INGEST_LISTEN="127.0.0.1:${INGEST_PORT}" \
      CLICKHOUSE_ADDR="127.0.0.1:${CH_PORT}" \
      CLICKHOUSE_USER=default \
      OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="http://127.0.0.1:${OTLP_GRPC_PORT}" \
      IP2ASN_PATH="$IP2ASN" \
      "$ROOT/$INGEST" >>"$(logfile ingest)" 2>&1 &
    echo $! >"$(pidfile ingest)"
    await_ready ingest 60 || fail_leg ingest
    ;;
  devissuer)
    STARTED="devissuer $STARTED"
    PORT="$ISSUER_PORT" "$ROOT/$DEVISSUER" >>"$(logfile devissuer)" 2>&1 &
    echo $! >"$(pidfile devissuer)"
    await_ready devissuer 60 || fail_leg devissuer
    ;;
  park)
    STARTED="park $STARTED"
    mkdir -p "$ROOT/src/chunkies/assets"
    park_database_url="$(scripts/wum-dev-db.sh url)"
    DATABASE_URL="$park_database_url" \
      CHUNK_NAME=park-mythra \
      TRUNK_PORT="$PARK_PORT" \
      HTTP_PORT="$PARK_HTTP_PORT" \
      METRICS_PORT="$PARK_METRICS_PORT" \
      INTERNAL_KEY_FILE="$RUN_DIR/internal.key" \
      BEHAVIOR_DIR="$ROOT/src/chunkies/behaviors" \
      GAME_MANIFEST_FILE="$ROOT/src/games/wake-up-mythra/services/wum/game.conf" \
      GENESIS_FILE="$ROOT/src/games/wake-up-mythra/services/wum/fixture_park.bin" \
      TICK_HZ="$DEV_TICK_HZ" \
      CHUNKIES_DEV_LIVE_TICK_RATE=true \
      OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="http://127.0.0.1:${OTLP_GRPC_PORT}" \
      "$ROOT/$PARK" >>"$(logfile park)" 2>&1 &
    echo $! >"$(pidfile park)"
    await_ready park 60 || fail_leg park
    ;;
  gateway)
    STARTED="gateway $STARTED"
    printf 'wum park-mythra 127.0.0.1:%s\n' "$PARK_PORT" >"$RUN_DIR/chunks.conf"
    OIDC_ISSUER="$ISSUER" \
      OIDC_CLIENT_IDS=wake-up-mythra \
      GAME=wum \
      DEFAULT_CHUNK=park-mythra \
      BEHAVIOR_DIR="$ROOT/src/chunkies/behaviors" \
      ASSET_DIR="$ROOT/src/chunkies/assets" \
      PUBLIC_ADDR="${WUM_DEV_PUBLIC_ADDR:-127.0.0.1:${GATEWAY_WT_PORT}}" \
      HTTP_PORT="$GATEWAY_HTTP_PORT" \
      METRICS_PORT="$GATEWAY_METRICS_PORT" \
      WT_PORT="$GATEWAY_WT_PORT" \
      CHUNK_DIRECTORY_FILE="$RUN_DIR/chunks.conf" \
      CHUNKIE_HTTP_URL="http://127.0.0.1:${PARK_HTTP_PORT}" \
      INTERNAL_KEY_FILE="$RUN_DIR/internal.key" \
      CHUNKIES_DEV_LIVE_TICK_RATE=true \
      OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="http://127.0.0.1:${OTLP_GRPC_PORT}" \
      "$ROOT/$GATEWAY" >>"$(logfile gateway)" 2>&1 &
    echo $! >"$(pidfile gateway)"
    await_ready gateway 60 || fail_leg gateway
    ;;
  web)
    STARTED="web $STARTED"
    (cd "$APP" && exec env VITE_OIDC_ISSUER="$ISSUER" WUM_DEV_INGEST_PORT="$INGEST_PORT" WUM_DEV_GATEWAY_HTTP_PORT="$GATEWAY_HTTP_PORT" vp dev) >>"$(logfile web)" 2>&1 &
    echo $! >"$(pidfile web)"
    await_ready web 240 || fail_leg web
    ;;
  esac
}

# Runs on every up, not only when this up started ch: a ClickHouse that
# survived an interrupted run may predate the schema, and every statement is
# IF NOT EXISTS.
apply_ch_ddl() {
  {
    "$ROOT/$CLICKHOUSE" client --port "$CH_PORT" --query "CREATE DATABASE IF NOT EXISTS guardian_analytics" &&
      "$ROOT/$CLICKHOUSE" client --port "$CH_PORT" --multiquery <"$ROOT/src/infrastructure/analytics/events-table.sql" &&
      "$ROOT/$CLICKHOUSE" client --port "$CH_PORT" --multiquery <"$ROOT/src/infrastructure/analytics/otel-traces-single-node.sql"
  } >>"$(logfile ch)" 2>&1 || fail_leg ch
}

up() {
  missing=""
  command -v bazelisk >/dev/null || {
    echo "wum-dev: bazelisk is required (see scripts/bootstrap.sh: eval \"\$(scripts/bootstrap.sh path)\")" >&2
    missing=1
  }
  command -v vp >/dev/null || {
    echo "wum-dev: vp (vite-plus) is required for the web app — install it, then run 'vp install' at the repo root" >&2
    missing=1
  }
  [ -z "$missing" ] || exit 1

  acquire_lock
  echo "wum-dev: building Chunkies + devissuer + telemetry legs…" >&2
  bazelisk build //src/chunkies:chunkies-gateway //src/chunkies:chunkies-chunkie //src/chunkies/devissuer \
    //src/analytics/ingest @ip2asn_combined//file \
    @multitool//tools/clickhouse-server @multitool//tools/otelcol-contrib @multitool//tools/flagd >&2
  GATEWAY="$(bazelisk cquery --output=files //src/chunkies:chunkies-gateway 2>/dev/null | head -1)"
  PARK="$(bazelisk cquery --output=files //src/chunkies:chunkies-chunkie 2>/dev/null | head -1)"
  DEVISSUER="$(bazelisk cquery --output=files //src/chunkies/devissuer 2>/dev/null | head -1)"
  INGEST="$(bazelisk cquery --output=files //src/analytics/ingest 2>/dev/null | head -1)"
  CLICKHOUSE="$(bazelisk cquery --output=files @multitool//tools/clickhouse-server 2>/dev/null | head -1)"
  OTELCOL="$(bazelisk cquery --output=files @multitool//tools/otelcol-contrib 2>/dev/null | head -1)"
  FLAGD="$(bazelisk cquery --output=files @multitool//tools/flagd 2>/dev/null | head -1)"
  # Joined against output_base, not execution_root: later bazel invocations
  # (the pg leg builds initdb) prune execroot external symlinks they don't use.
  IP2ASN="$(bazelisk info output_base 2>/dev/null)/$(bazelisk cquery --output=files @ip2asn_combined//file 2>/dev/null | head -1)"
  if [ ! -s "$RUN_DIR/internal.key" ]; then
    (umask 077; dd if=/dev/urandom bs=32 count=1 status=none | base64 >"$RUN_DIR/internal.key")
  fi
  for leg in $LEGS; do
    start_leg "$leg"
    if [ "$leg" = ch ]; then
      apply_ch_ddl
    fi
  done
  echo >&2
  echo "wum-dev: up — open http://127.0.0.1:${WEB_PORT} and sign in as any name" >&2
  echo "  aspect mythra dev status" >&2
  echo "  aspect mythra dev logs --leg=gateway" >&2
  echo "  aspect mythra dev down" >&2
  echo "  events:    $ROOT/$CLICKHOUSE client --port $CH_PORT --query 'SELECT count() FROM guardian_analytics.events'" >&2
  echo "  spans:     $ROOT/$CLICKHOUSE client --port $CH_PORT --query 'SELECT count() FROM guardian_analytics.otel_traces'" >&2
  echo "  flags:     edit $FLAGS_FILE (flagd hot-reloads it)" >&2
  echo "  tick rate: ${DEV_TICK_HZ}Hz startup; 'aspect mythra dev latency' journals a live 24->48Hz boundary" >&2
  echo >&2
}

down() {
  acquire_lock
  rc=0
  for leg in web gateway park devissuer ingest flagd otelcol ch pg; do
    stop_leg "$leg" || {
      echo "wum-dev: $leg refused to stop" >&2
      rc=1
    }
  done
  [ "$rc" -eq 0 ] && echo "wum-dev: down" >&2
  return "$rc"
}

status() {
  rc=0
  for leg in $LEGS; do
    pid="$(leg_pid "$leg")"
    if ! leg_alive "$leg"; then
      state=down
      pid="-"
      rc=1
    elif probe "$leg"; then
      state=healthy
    else
      state=degraded
      rc=1
    fi
    printf '%-10s %-8s %-6s %s\n' "$leg" "$pid" "$(leg_port "$leg")" "$state"
  done
  return "$rc"
}

# Reads only: the stack was built and started by up, so smoke resolves the
# already-built client instead of triggering a build.
resolve_clickhouse() {
  CLICKHOUSE="$(bazelisk cquery --output=files @multitool//tools/clickhouse-server 2>/dev/null | head -1)"
  [ -n "$CLICKHOUSE" ] && [ -x "$ROOT/$CLICKHOUSE" ]
}

ch_query() {
  "$ROOT/$CLICKHOUSE" client --port "$CH_PORT" --query "$1"
}

# The dev stack's own canary: a headless player signs in and connects, then
# both telemetry lanes must land in the local ClickHouse. Never mutates
# stack state beyond the one journey it drives.
smoke() {
  if ! status_output="$(status)"; then
    printf '%s\n' "$status_output" >&2
    unhealthy="$(printf '%s\n' "$status_output" | awk '$4 != "healthy" { print $1 }' | paste -sd' ' -)"
    echo "wum-dev: smoke needs a healthy stack (unhealthy: $unhealthy) — start it with: aspect mythra dev up" >&2
    return 1
  fi

  if ! resolve_clickhouse; then
    echo "wum-dev: cannot resolve the pinned clickhouse client — run: aspect mythra dev up" >&2
    return 1
  fi

  pw_browser="$(cd "$APP" && node -e 'console.log(require("playwright").chromium.executablePath())' 2>/dev/null)" || true
  if [ -z "$pw_browser" ]; then
    echo "wum-dev: the app's playwright package is missing — run 'vp install' at the repo root" >&2
    return 1
  fi
  if [ ! -x "$pw_browser" ]; then
    echo "wum-dev: playwright's chromium is not installed — install it with: cd $APP && npx playwright install chromium" >&2
    return 1
  fi

  # Run scoping: the trace lane is scoped by this run's unique player name
  # (it rides the span as wum.sub); the events lane by the page id the
  # probe page minted (plus the sink's clock) — no other session's rows,
  # concurrent smoke, or localStorage replay of a stale queue can satisfy
  # either.
  player="smoke-$(date +%s)-$$"
  t0="$(ch_query 'SELECT toUnixTimestamp64Milli(now64(3))')" || {
    echo "wum-dev: ClickHouse did not answer on port $CH_PORT" >&2
    return 1
  }

  smoke_log="$RUN_DIR/smoke.log"
  : >"$smoke_log"
  echo "wum-dev: smoke — headless player '$player' joining the park…" >&2
  if ! (cd "$APP" && WUM_SMOKE_PLAYER="$player" node e2e/smoke.mjs) >>"$smoke_log" 2>&1; then
    echo "wum-dev: SMOKE FAIL — the journey broke (browser → devissuer → gateway → park); journey log tail:" >&2
    tail -n 20 "$smoke_log" >&2
    echo "wum-dev: gateway log tail:" >&2
    tail -n 10 "$(logfile gateway)" >&2
    echo "wum-dev: park log tail:" >&2
    tail -n 10 "$(logfile park)" >&2
    return 1
  fi
  dial_ms="$(sed -n 's/^SMOKE_JOURNEY dial_ms=\([0-9]*\).*/\1/p' "$smoke_log" | tail -1)"
  page_id="$(sed -n 's/^SMOKE_JOURNEY .*page_id=\([0-9a-f]\{16\}\).*/\1/p' "$smoke_log" | tail -1)"
  if [ -z "$page_id" ]; then
    echo "wum-dev: SMOKE FAIL — journey did not report its page id; journey log tail:" >&2
    tail -n 20 "$smoke_log" >&2
    return 1
  fi

  # The page is gone, but the beacon's payload still crosses the ingest's
  # 10s batcher and the spans ride the collector's own batcher — so both
  # lanes get one bounded poll instead of one hopeful sleep.
  events=0
  spans=0
  poll_deadline=$((SECONDS + 60))
  while :; do
    events="$(ch_query "SELECT count() FROM guardian_analytics.events WHERE event_name = 'wum.connected' AND page_id = unhex('$page_id') AND toUnixTimestamp64Milli(server_ts) >= $t0")" || events=0
    spans="$(ch_query "SELECT count() FROM guardian_analytics.otel_traces WHERE ServiceName = 'chunkies-gateway' AND SpanName = 'POST /session' AND SpanAttributes['wum.sub'] = '$player'")" || spans=0
    if [ "$events" -ge 1 ] && [ "$spans" -ge 1 ]; then
      break
    fi
    [ "$SECONDS" -lt "$poll_deadline" ] || break
    sleep 2
  done

  rc=0
  if [ "$events" -lt 1 ]; then
    echo "wum-dev: SMOKE FAIL — events lane: no wum.connected row for page $page_id landed (beacon → web /api/events proxy → ingest → ClickHouse); ingest log tail:" >&2
    tail -n 20 "$(logfile ingest)" >&2
    rc=1
  fi
  if [ "$spans" -lt 1 ]; then
    echo "wum-dev: SMOKE FAIL — trace lane: no chunkies-gateway 'POST /session' span for $player; otelcol log tail:" >&2
    tail -n 20 "$(logfile otelcol)" >&2
    rc=1
  fi
  [ "$rc" -eq 0 ] || return "$rc"

  echo "wum-dev: SMOKE PASS — dial ${dial_ms}ms; +$events wum.connected event(s), +$spans chunkies-gateway 'POST /session' span(s)"
}

# Measures the actions the product exposes today on both sides of one live
# 24->48Hz journal boundary. The same browser page must observe rate_set and
# continue without redial, resync, restore, or reload. Client facts cover first
# wire write -> local apply; authority spans split receipt -> next tick from the
# rest of durable fan-out.
latency() {
  if ! status_output="$(status)"; then
    printf '%s\n' "$status_output" >&2
    echo "wum-dev: latency needs a healthy stack — start it with: aspect mythra dev up" >&2
    return 1
  fi
  if ! resolve_clickhouse; then
    echo "wum-dev: cannot resolve the pinned clickhouse client — run: aspect mythra dev up" >&2
    return 1
  fi

  player="latency-$(date +%s)-$$"
  t0="$(ch_query 'SELECT toUnixTimestamp64Milli(now64(3))')" || {
    echo "wum-dev: ClickHouse did not answer on port $CH_PORT" >&2
    return 1
  }
  latency_log="$RUN_DIR/latency.log"
  : >"$latency_log"
  echo "wum-dev: latency — one connected player crossing a live 24Hz -> 48Hz boundary…" >&2
  if ! (cd "$APP" && WUM_LATENCY_PLAYER="$player" WUM_RATE_CONTROL_URL="http://127.0.0.1:${GATEWAY_HTTP_PORT}/dev/tick-rate" node e2e/latency.mjs) >"$latency_log" 2>&1; then
    echo "wum-dev: LATENCY FAIL — browser journey broke; journey log:" >&2
    cat "$latency_log" >&2
    return 1
  fi
  cat "$latency_log"
  rates="$(sed -n 's/^LATENCY_JOURNEY rates=\([0-9]*,[0-9]*\).*/\1/p' "$latency_log" | tail -1)"
  actions="$(sed -n 's/^LATENCY_JOURNEY .* actions=\([0-9]*\).*/\1/p' "$latency_log" | tail -1)"
  from_hz="${rates%,*}"
  to_hz="${rates#*,}"
  if [ -z "$rates" ] || [ "$from_hz" = "$to_hz" ] || [ -z "$actions" ]; then
    echo "wum-dev: LATENCY FAIL — journey did not report two rates and its action count" >&2
    return 1
  fi

  # The park and collector each batch asynchronously. Bound the wait and
  # require every client-observed accepted action to have its server fact;
  # a partial trace sample is not a latency measurement.
  server_actions=0
  poll_deadline=$((SECONDS + 60))
  while :; do
    server_actions="$(ch_query "SELECT count() FROM guardian_analytics.otel_traces WHERE SpanName = 'chunkies.intent' AND SpanAttributes['chunkies.result'] = 'accepted' AND SpanAttributes['chunkies.rate_hz'] IN ('$from_hz', '$to_hz') AND toUnixTimestamp64Milli(Timestamp) >= $t0")" || server_actions=0
    [ "$server_actions" -ge "$actions" ] && break
    [ "$SECONDS" -lt "$poll_deadline" ] || break
    sleep 2
  done
  if [ "$server_actions" -lt "$actions" ]; then
    echo "wum-dev: LATENCY FAIL — only $server_actions/$actions accepted chunkies.intent spans landed" >&2
    return 1
  fi

  echo "SERVER_ACTIONS (receipt -> next tick -> durable fan-out)"
  ch_query "SELECT SpanAttributes['chunkies.rate_hz'] AS rate_hz, SpanAttributes['chunkies.kind'] AS kind, count() AS n, round(quantileExact(0.5)(toFloat64OrZero(SpanAttributes['chunkies.tick_queue_ms'])), 2) AS queue_p50_ms, round(quantileExact(0.95)(toFloat64OrZero(SpanAttributes['chunkies.tick_queue_ms'])), 2) AS queue_p95_ms, round(quantileExact(0.5)(toFloat64OrZero(SpanAttributes['chunkies.authority_ms'])), 2) AS authority_p50_ms, round(quantileExact(0.95)(toFloat64OrZero(SpanAttributes['chunkies.authority_ms'])), 2) AS authority_p95_ms FROM guardian_analytics.otel_traces WHERE SpanName = 'chunkies.intent' AND SpanAttributes['chunkies.result'] = 'accepted' AND SpanAttributes['chunkies.rate_hz'] IN ('$from_hz', '$to_hz') AND toUnixTimestamp64Milli(Timestamp) >= $t0 GROUP BY rate_hz, kind ORDER BY toUInt32(rate_hz), kind FORMAT PrettyCompactNoEscapes"
  echo "wum-dev: LATENCY PASS — one page adopted ${from_hz}Hz -> ${to_hz}Hz; $actions client actions and $server_actions authority spans"
}

logs() {
  leg="${1:-}"
  if [ -z "$leg" ]; then
    tail -n 40 "$RUN_DIR"/*.log 2>/dev/null || echo "wum-dev: no logs yet — run 'aspect mythra dev up' first" >&2
    return 0
  fi
  case " $LEGS " in
  *" $leg "*) ;;
  *)
    echo "wum-dev: unknown leg '$leg' (expected one of: $LEGS)" >&2
    return 2
    ;;
  esac
  tail -n 100 -f "$(logfile "$leg")"
}

case "${1:-up}" in
up) up ;;
down) down ;;
status) status ;;
logs) logs "${2:-}" ;;
smoke) smoke ;;
latency) latency ;;
*)
  echo "usage: $0 [up|down|status|logs [leg]|smoke|latency]" >&2
  exit 2
  ;;
esac

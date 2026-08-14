#!/usr/bin/env bash
# Wake Up Mythra local development stack, fronted by `aspect mythra dev`:
#
#   scripts/wum-dev.sh [up|down|status|logs [leg]]
#
# Everything mirrors prod: same admission path, same module distribution,
# same ingress split.
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$PWD"
APP="$ROOT/src/products/viteplus-monorepo/apps/wake-up-mythra-web"
RUN_DIR="$ROOT/.guardian/dev/wum"
ISSUER_PORT="${WUM_DEV_ISSUER_PORT:-9635}"
PG_PORT="${WUM_DEV_PG_PORT:-55432}"
CH_PORT="${WUM_DEV_CH_PORT:-59000}"
CH_HTTP_PORT="${WUM_DEV_CH_HTTP_PORT:-58123}"
INGEST_PORT="${WUM_DEV_INGEST_PORT:-9636}"
ISSUER="http://127.0.0.1:${ISSUER_PORT}/realms/dev"
MYTHRAD_HTTP_PORT=9634
MYTHRAD_METRICS_PORT=9633
WEB_PORT=4254
OTLP_GRPC_PORT="${WUM_DEV_OTLP_GRPC_PORT:-4317}"
OTLP_HTTP_PORT="${WUM_DEV_OTLP_HTTP_PORT:-4318}"
FLAGD_PORT="${WUM_DEV_FLAGD_PORT:-8013}"
FLAGD_MGMT_PORT="${WUM_DEV_FLAGD_MGMT_PORT:-8014}"
# The web app's dev flag client hardcodes OFREP at 127.0.0.1:8016.
FLAGD_OFREP_PORT=8016
FLAGS_FILE="$ROOT/src/infrastructure/deployments/flags/prod/flags/flags.json"

LEGS="pg ch otelcol flagd ingest devissuer mythrad web"
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
  mythrad) echo "mythrad" ;;
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
  mythrad) echo "$MYTHRAD_HTTP_PORT" ;;
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
  mythrad) curl -fsS --max-time 2 "http://127.0.0.1:${MYTHRAD_METRICS_PORT}/readyz" >/dev/null 2>&1 ;;
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
  echo "wum-dev: starting $leg…" >&2
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
      OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="127.0.0.1:${OTLP_GRPC_PORT}" \
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
  mythrad)
    STARTED="mythrad $STARTED"
    mkdir -p "$ROOT/src/services/mythrad/assets"
    DATABASE_URL="$(scripts/wum-dev-db.sh url)" \
      OIDC_ISSUER="$ISSUER" \
      BEHAVIOR_DIR="$ROOT/src/services/mythrad/behaviors" \
      ASSET_DIR="$ROOT/src/services/mythrad/assets" \
      PUBLIC_ADDR="${WUM_DEV_PUBLIC_ADDR:-127.0.0.1:4433}" \
      OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="127.0.0.1:${OTLP_GRPC_PORT}" \
      "$ROOT/$MYTHRAD" >>"$(logfile mythrad)" 2>&1 &
    echo $! >"$(pidfile mythrad)"
    await_ready mythrad 60 || fail_leg mythrad
    ;;
  web)
    STARTED="web $STARTED"
    (cd "$APP" && exec env VITE_OIDC_ISSUER="$ISSUER" WUM_DEV_INGEST_PORT="$INGEST_PORT" vp dev) >>"$(logfile web)" 2>&1 &
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
    echo "wum-dev: vp (vite-plus) is required for the web app — install it, then run 'vp install' in src/products/viteplus-monorepo" >&2
    missing=1
  }
  [ -z "$missing" ] || exit 1

  acquire_lock
  echo "wum-dev: building mythrad + devissuer + telemetry legs…" >&2
  bazelisk build //src/services/mythrad //src/services/mythrad/devissuer \
    //src/products/analytics/ingest @ip2asn_combined//file \
    @multitool//tools/clickhouse-server @multitool//tools/otelcol-contrib @multitool//tools/flagd >&2
  MYTHRAD="$(bazelisk cquery --output=files //src/services/mythrad 2>/dev/null | head -1)"
  DEVISSUER="$(bazelisk cquery --output=files //src/services/mythrad/devissuer 2>/dev/null | head -1)"
  INGEST="$(bazelisk cquery --output=files //src/products/analytics/ingest 2>/dev/null | head -1)"
  CLICKHOUSE="$(bazelisk cquery --output=files @multitool//tools/clickhouse-server 2>/dev/null | head -1)"
  OTELCOL="$(bazelisk cquery --output=files @multitool//tools/otelcol-contrib 2>/dev/null | head -1)"
  FLAGD="$(bazelisk cquery --output=files @multitool//tools/flagd 2>/dev/null | head -1)"
  # Joined against output_base, not execution_root: later bazel invocations
  # (the pg leg builds initdb) prune execroot external symlinks they don't use.
  IP2ASN="$(bazelisk info output_base 2>/dev/null)/$(bazelisk cquery --output=files @ip2asn_combined//file 2>/dev/null | head -1)"
  for leg in $LEGS; do
    start_leg "$leg"
    if [ "$leg" = ch ]; then
      apply_ch_ddl
    fi
  done
  echo >&2
  echo "wum-dev: up — open http://127.0.0.1:${WEB_PORT} and sign in as any name" >&2
  echo "  aspect mythra dev status" >&2
  echo "  aspect mythra dev logs --leg=mythrad" >&2
  echo "  aspect mythra dev down" >&2
  echo "  events:    $ROOT/$CLICKHOUSE client --port $CH_PORT --query 'SELECT count() FROM guardian_analytics.events'" >&2
  echo "  spans:     $ROOT/$CLICKHOUSE client --port $CH_PORT --query 'SELECT count() FROM guardian_analytics.otel_traces'" >&2
  echo "  flags:     edit $FLAGS_FILE (flagd hot-reloads it)" >&2
  echo >&2
}

down() {
  acquire_lock
  rc=0
  for leg in web mythrad devissuer ingest flagd otelcol ch pg; do
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
*)
  echo "usage: $0 [up|down|status|logs [leg]]" >&2
  exit 2
  ;;
esac

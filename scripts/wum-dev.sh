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
ISSUER="http://127.0.0.1:${ISSUER_PORT}/realms/dev"
MYTHRAD_HTTP_PORT=9634
MYTHRAD_METRICS_PORT=9633
WEB_PORT=4254

LEGS="pg devissuer mythrad web"
STARTED=""

pidfile() { echo "$RUN_DIR/$1.pid"; }
logfile() { echo "$RUN_DIR/$1.log"; }

leg_pid() {
  cat "$(pidfile "$1")" 2>/dev/null || true
}

leg_command_pattern() {
  case "$1" in
  pg) echo "postgres" ;;
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
  devissuer) echo "$ISSUER_PORT" ;;
  mythrad) echo "$MYTHRAD_HTTP_PORT" ;;
  web) echo "$WEB_PORT" ;;
  esac
}

probe() {
  case "$1" in
  pg) [ "$(leg_pid pg)" = "$(head -1 "$RUN_DIR/pgdata/postmaster.pid" 2>/dev/null)" ] && (exec 3<>"/dev/tcp/127.0.0.1/$PG_PORT") 2>/dev/null ;;
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
      "$ROOT/$MYTHRAD" >>"$(logfile mythrad)" 2>&1 &
    echo $! >"$(pidfile mythrad)"
    await_ready mythrad 60 || fail_leg mythrad
    ;;
  web)
    STARTED="web $STARTED"
    (cd "$APP" && exec env VITE_OIDC_ISSUER="$ISSUER" vp dev) >>"$(logfile web)" 2>&1 &
    echo $! >"$(pidfile web)"
    await_ready web 240 || fail_leg web
    ;;
  esac
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
  echo "wum-dev: building mythrad + devissuer…" >&2
  bazelisk build //src/services/mythrad //src/services/mythrad/devissuer >&2
  MYTHRAD="$(bazelisk cquery --output=files //src/services/mythrad 2>/dev/null | head -1)"
  DEVISSUER="$(bazelisk cquery --output=files //src/services/mythrad/devissuer 2>/dev/null | head -1)"
  for leg in $LEGS; do
    start_leg "$leg"
  done
  echo >&2
  echo "wum-dev: up — open http://127.0.0.1:${WEB_PORT} and sign in as any name" >&2
  echo "  aspect mythra dev status" >&2
  echo "  aspect mythra dev logs --leg=mythrad" >&2
  echo "  aspect mythra dev down" >&2
  echo >&2
}

down() {
  acquire_lock
  rc=0
  for leg in web mythrad devissuer pg; do
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

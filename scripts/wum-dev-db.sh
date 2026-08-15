#!/usr/bin/env bash
# Local journal database for Wake Up Mythra development. An empty database
# is a complete environment: parks genesis themselves on first open, so
# contributors need no data. `from-prod` is the maintainer path — it wipes
# and re-seeds from the production journal so a prod park's history can be
# replayed and diffed through locally edited code.
#
#   scripts/wum-dev-db.sh [start|stop|wipe|from-prod|url]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="$ROOT/.guardian/dev/wum"
DATA_DIR="$RUN_DIR/pgdata"
LOG_FILE="$RUN_DIR/pg.log"
PID_FILE="$RUN_DIR/pg.pid"
SOCKET_DIR=/tmp/wum-dev-pg
PORT="${WUM_DEV_PG_PORT:-55432}"
DSN="postgresql://mythra:mythra@127.0.0.1:${PORT}/mythra?sslmode=disable"

pg_bin_dir() {
  (cd "$ROOT" && bazelisk build //src/tools/postgres:initdb >&2)
  initdb_rel="$(cd "$ROOT" && bazelisk cquery --output=files //src/tools/postgres:initdb 2>/dev/null | head -1)"
  execroot="$(cd "$ROOT" && bazelisk info execution_root 2>/dev/null)"
  [ -n "$initdb_rel" ] && [ -n "$execroot" ] || {
    echo "wum-dev-db: could not resolve //src/tools/postgres:initdb" >&2
    exit 1
  }
  dirname "$(readlink -f "$execroot/$initdb_rel")"
}

case "${1:-start}" in
start | wipe | from-prod)
  BIN="$(pg_bin_dir)"
  ;;
esac

running() {
  "$BIN/pg_ctl" -D "$DATA_DIR" status >/dev/null 2>&1
}

postmaster_pid() {
  head -1 "$DATA_DIR/postmaster.pid" 2>/dev/null || true
}

postmaster_alive() {
  [ -n "$1" ] || return 1
  case "$(ps -p "$1" -o comm= 2>/dev/null)" in
  postgres | */postgres) return 0 ;;
  esac
  return 1
}

start() {
  mkdir -p "$RUN_DIR" "$SOCKET_DIR"
  if [ ! -f "$DATA_DIR/PG_VERSION" ]; then
    rm -rf "$DATA_DIR"
    "$BIN/initdb" -D "$DATA_DIR" -U mythra -A trust --no-sync -E UTF8 --locale=C >>"$LOG_FILE" 2>&1
  fi
  if ! running; then
    "$BIN/pg_ctl" -D "$DATA_DIR" -l "$LOG_FILE" -w -t 60 \
      -o "-k $SOCKET_DIR -c listen_addresses=127.0.0.1 -c port=$PORT -F" start >&2
  fi
  ready=""
  for _ in $(seq 1 60); do
    if "$BIN/pg_isready" -h 127.0.0.1 -p "$PORT" -U mythra -d postgres >/dev/null 2>&1; then
      ready=1
      break
    fi
    sleep 0.5
  done
  if [ -z "$ready" ]; then
    echo "wum-dev-db: postgres did not become ready" >&2
    tail -n 20 "$LOG_FILE" >&2 || true
    exit 1
  fi
  if ! "$BIN/psql" -h "$SOCKET_DIR" -p "$PORT" -U mythra -d postgres -tAc \
    "SELECT 1 FROM pg_database WHERE datname='mythra'" 2>/dev/null | grep -q 1; then
    "$BIN/createdb" -h "$SOCKET_DIR" -p "$PORT" -U mythra mythra >&2
  fi
  head -1 "$DATA_DIR/postmaster.pid" >"$PID_FILE"
  echo "wum-dev-db: ready on 127.0.0.1:${PORT}" >&2
}

stop() {
  pid="$(postmaster_pid)"
  if ! postmaster_alive "$pid"; then
    rm -f "$PID_FILE"
    return 0
  fi
  kill -INT "$pid"
  for _ in $(seq 1 120); do
    if ! kill -0 "$pid" 2>/dev/null; then
      rm -f "$PID_FILE"
      return 0
    fi
    sleep 0.5
  done
  echo "wum-dev-db: postgres (pid $pid) did not stop" >&2
  return 1
}

case "${1:-start}" in
start)
  start
  ;;
stop)
  stop
  ;;
wipe)
  stop
  rm -rf "$DATA_DIR"
  start
  ;;
from-prod)
  # The JIT Mythra observer runs pg_dump in the credential-isolated console;
  # neither this process nor the standing platform-agent reads a Secret.
  start
  echo "wum-dev-db: dumping prod mythra journal…" >&2
  "$BIN/psql" -h "$SOCKET_DIR" -p "$PORT" -U mythra -d postgres -q \
    -c "DROP DATABASE IF EXISTS mythra WITH (FORCE)" -c "CREATE DATABASE mythra OWNER mythra"
  (cd "$ROOT" && aspect mythra dump) |
    "$BIN/psql" -q -v ON_ERROR_STOP=1 -h "$SOCKET_DIR" -p "$PORT" -U mythra -d mythra
  echo "wum-dev-db: prod journal restored locally" >&2
  ;;
url)
  echo "$DSN"
  ;;
*)
  echo "usage: $0 [start|stop|wipe|from-prod|url]" >&2
  exit 2
  ;;
esac

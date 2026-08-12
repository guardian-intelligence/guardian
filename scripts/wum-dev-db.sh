#!/usr/bin/env bash
# Local journal database for Wake Up Mythra development. An empty database
# is a complete environment: parks genesis themselves on first open, so
# contributors need no data. `from-prod` is the maintainer path — it wipes
# and re-seeds from the production journal so a prod park's history can be
# replayed and diffed through locally edited code.
#
#   scripts/wum-dev-db.sh [start|stop|wipe|from-prod|url]
set -euo pipefail

NAME=wum-dev-pg
VOLUME=wum-dev-pg-data
PORT="${WUM_DEV_PG_PORT:-55432}"
IMAGE=postgres:16-alpine
DSN="postgresql://mythra:mythra@127.0.0.1:${PORT}/mythra?sslmode=disable"

wait_ready() {
  for _ in $(seq 1 60); do
    if docker exec "$NAME" pg_isready -U mythra -d mythra >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
  done
  echo "wum-dev-db: postgres did not become ready" >&2
  exit 1
}

start() {
  if [ "$(docker ps -q -f "name=^${NAME}$")" ]; then
    :
  elif [ "$(docker ps -aq -f "name=^${NAME}$")" ]; then
    docker start "$NAME" >/dev/null
  else
    docker run -d --name "$NAME" \
      -e POSTGRES_USER=mythra -e POSTGRES_PASSWORD=mythra -e POSTGRES_DB=mythra \
      -p "127.0.0.1:${PORT}:5432" \
      -v "${VOLUME}:/var/lib/postgresql/data" \
      "$IMAGE" >/dev/null
  fi
  wait_ready
  echo "wum-dev-db: ready on 127.0.0.1:${PORT}" >&2
}

case "${1:-start}" in
start)
  start
  ;;
stop)
  docker stop "$NAME" >/dev/null 2>&1 || true
  ;;
wipe)
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  docker volume rm "$VOLUME" >/dev/null 2>&1 || true
  start
  ;;
from-prod)
  # Requires cluster read access (docs/agent-environment-authentication.md)
  # and the postgres credentials Secret. The dump runs through pg_dump
  # inside the local container so no postgres client is needed on the host.
  start
  echo "wum-dev-db: dumping prod mythra journal…" >&2
  # The password comes from the operator (most personas cannot read the
  # Secret); kubectl is the fallback for personas that can.
  PW="${WUM_PROD_PG_PASSWORD:-}"
  if [ -z "$PW" ]; then
    PW="$(kubectl get secret -n tenant-guardian-prod postgres-products-credentials \
      -o jsonpath='{.data.mythra}' | base64 -d)" || {
      echo "wum-dev-db: cannot read the prod credentials Secret with this persona;" >&2
      echo "set WUM_PROD_PG_PASSWORD (see docs/agent-environment-authentication.md)" >&2
      exit 1
    }
  fi
  kubectl port-forward -n tenant-guardian-prod svc/postgres-products-rw 55433:5432 >/dev/null 2>&1 &
  PF=$!
  trap 'kill $PF 2>/dev/null' EXIT
  for _ in $(seq 1 30); do
    docker exec -e PGPASSWORD="$PW" "$NAME" \
      pg_isready -h host.docker.internal -p 55433 -U mythra >/dev/null 2>&1 && break
    sleep 1
  done
  docker exec "$NAME" psql -U mythra -d postgres -q \
    -c "DROP DATABASE IF EXISTS mythra WITH (FORCE)" -c "CREATE DATABASE mythra OWNER mythra"
  docker exec -e PGPASSWORD="$PW" "$NAME" sh -c \
    "pg_dump -h host.docker.internal -p 55433 -U mythra -d mythra --no-owner --no-privileges | psql -q -U mythra -d mythra"
  echo "wum-dev-db: prod journal restored locally" >&2
  ;;
url)
  ;;
*)
  echo "usage: $0 [start|stop|wipe|from-prod|url]" >&2
  exit 2
  ;;
esac

echo "$DSN"

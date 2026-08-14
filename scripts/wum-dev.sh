#!/usr/bin/env bash
# One-command Wake Up Mythra development environment:
#
#   scripts/wum-dev.sh
#
# Starts (and tears down together on Ctrl-C):
#   - postgres in docker (empty journal: parks genesis themselves)
#   - the dev OIDC issuer on 127.0.0.1:9635 (sign in as any name;
#     mythrad validates through its real oidcGate path)
#   - mythrad on 127.0.0.1:9634 (HTTP) / :4433 (WebTransport, self-signed
#     cert pinned via certHashB64) reading wasm from the in-repo behavior
#     dir — `bazelisk run //src/services/mythrad/sim:refresh` while this is
#     running hot-swaps the park module into the live server
#   - the vite dev server on http://127.0.0.1:4254 (open this)
#
# Everything mirrors prod: same admission path, same module distribution,
# same ingress split (vite proxies /session /wt-info /behavior /assets to
# mythrad exactly as the prod Ingress does).
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$PWD"
APP="$ROOT/src/products/viteplus-monorepo/apps/wake-up-mythra-web"
ISSUER_PORT="${WUM_DEV_ISSUER_PORT:-9635}"
ISSUER="http://127.0.0.1:${ISSUER_PORT}/realms/dev"

command -v docker >/dev/null || { echo "wum-dev: docker is required for the journal db" >&2; exit 1; }
command -v bazelisk >/dev/null || { echo "wum-dev: bazelisk is required (see scripts/bootstrap.sh)" >&2; exit 1; }
command -v vp >/dev/null || { echo "wum-dev: vp (vite-plus) is required for the web app" >&2; exit 1; }

DSN="$(scripts/wum-dev-db.sh start)"

echo "wum-dev: building mythrad + devissuer…" >&2
bazelisk build //src/services/mythrad //src/services/mythrad/devissuer >&2
MYTHRAD="$(bazelisk cquery --output=files //src/services/mythrad 2>/dev/null | head -1)"
DEVISSUER="$(bazelisk cquery --output=files //src/services/mythrad/devissuer 2>/dev/null | head -1)"

PIDS=()
cleanup() {
  trap - EXIT INT TERM
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

PORT="$ISSUER_PORT" "$ROOT/$DEVISSUER" &
PIDS+=($!)

# Prod serves skins from the mythra-assets ConfigMap; locally, drop .svg
# files into this dir and mythrad's 2s poll picks them up live.
mkdir -p "$ROOT/src/services/mythrad/assets"

DATABASE_URL="$DSN" \
  OIDC_ISSUER="$ISSUER" \
  BEHAVIOR_DIR="$ROOT/src/services/mythrad/behaviors" \
  ASSET_DIR="$ROOT/src/services/mythrad/assets" \
  PUBLIC_ADDR="${WUM_DEV_PUBLIC_ADDR:-127.0.0.1:4433}" \
  "$ROOT/$MYTHRAD" &
PIDS+=($!)

echo >&2
echo "wum-dev: open http://127.0.0.1:4254  (sign in as any name; Ctrl-C stops everything)" >&2
echo >&2
cd "$APP" && VITE_OIDC_ISSUER="$ISSUER" vp dev

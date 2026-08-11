#!/usr/bin/env bash
# Regenerates the committed wasm behavior artifacts from source. Run via:
#   bazelisk run //src/services/mythrad/sim:refresh
set -euo pipefail

if [[ -z "${BUILD_WORKSPACE_DIRECTORY:-}" ]]; then
  echo "run this via: bazelisk run //src/services/mythrad/sim:refresh" >&2
  exit 1
fi

here="$(dirname "${BASH_SOURCE[0]}")"
svc="$BUILD_WORKSPACE_DIRECTORY/src/services/mythrad/behaviors"
prod="$BUILD_WORKSPACE_DIRECTORY/src/infrastructure/deployments/mythra/prod/behavior"

install -m 0644 "$here/server.wasm" "$svc/server.wasm"
install -m 0644 "$here/client.wasm" "$svc/client.wasm"
install -m 0644 "$here/park.wasm" "$svc/park.wasm"
mkdir -p "$BUILD_WORKSPACE_DIRECTORY/src/services/mythrad/terrain"
install -m 0644 "$here/fixture_park.bin" "$BUILD_WORKSPACE_DIRECTORY/src/services/mythrad/terrain/fixture_park.bin"
install -m 0644 "$here/server.wasm" "$prod/live.wasm"
install -m 0644 "$here/client.wasm" "$prod/client.wasm"
install -m 0644 "$here/park.wasm" "$prod/park.wasm"
echo "refreshed: $svc/{server,client,park}.wasm, terrain/fixture_park.bin, and $prod/{live,client,park}.wasm"

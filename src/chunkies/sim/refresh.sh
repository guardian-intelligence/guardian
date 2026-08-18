#!/usr/bin/env bash
# Regenerates the committed wasm behavior artifacts from source. Run via:
#   bazelisk run //src/chunkies/sim:refresh
set -euo pipefail

if [[ -z "${BUILD_WORKSPACE_DIRECTORY:-}" ]]; then
  echo "run this via: bazelisk run //src/chunkies/sim:refresh" >&2
  exit 1
fi

here="$(dirname "${BASH_SOURCE[0]}")"
# Data deps land under the runfiles root at their own package path, and the
# game's modules no longer live under this package.
root="$here/../../.."
game="$root/src/games/wake-up-mythra/sim"
svc="$BUILD_WORKSPACE_DIRECTORY/src/chunkies/mount/behaviors"
prod="$BUILD_WORKSPACE_DIRECTORY/src/infrastructure/deployments/mythra/prod/behavior"

install -m 0644 "$here/client/client.wasm" "$svc/client.wasm"
install -m 0644 "$game/park.wasm" "$svc/park.wasm"
install -m 0644 "$game/fixture_park.bin" "$BUILD_WORKSPACE_DIRECTORY/src/games/wake-up-mythra/services/wum/fixture_park.bin"
install -m 0644 "$here/client/client.wasm" "$prod/client.wasm"
install -m 0644 "$game/park.wasm" "$prod/park.wasm"
install -m 0644 "$here/client/telemetry.gen.ts" "$BUILD_WORKSPACE_DIRECTORY/src/chunkies/host/ts/src/telemetry.gen.ts"
mkdir -p "$BUILD_WORKSPACE_DIRECTORY/src/chunkies/testkit/ts/goldens"
install -m 0644 "$here/client/records.json" "$BUILD_WORKSPACE_DIRECTORY/src/chunkies/testkit/ts/goldens/records.json"
echo "refreshed: $svc/{client,park}.wasm, wum/fixture_park.bin, $prod/{client,park}.wasm, telemetry.gen.ts, and goldens/records.json"

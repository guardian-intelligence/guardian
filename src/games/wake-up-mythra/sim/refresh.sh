#!/usr/bin/env bash
# Regenerates WUM's committed behavior artifacts from source. Run via:
#   bazelisk run //src/games/wake-up-mythra/sim:refresh
set -euo pipefail

if [[ -z "${BUILD_WORKSPACE_DIRECTORY:-}" ]]; then
  echo "run this via: bazelisk run //src/games/wake-up-mythra/sim:refresh" >&2
  exit 1
fi

here="$(dirname "${BASH_SOURCE[0]}")"
prod="$BUILD_WORKSPACE_DIRECTORY/src/games/wake-up-mythra/deploy/prod/behavior"

install -m 0644 "$here/client/client.wasm" "$prod/client.wasm"
install -m 0644 "$here/park.wasm" "$prod/sim.wasm"
install -m 0644 "$here/fixture_park.bin" "$BUILD_WORKSPACE_DIRECTORY/src/games/wake-up-mythra/services/wum/fixture_park.bin"
# Two framework-facing tables ride the game's generators for now: the
# session-module emit/request vocabulary the TS host acts on, and the
# record-layout goldens the testkit replays. Both are held current by
# diff tests; both graduate to framework-owned definitions when a second
# game needs them.
install -m 0644 "$here/client/telemetry.gen.ts" "$BUILD_WORKSPACE_DIRECTORY/src/chunkies/host/ts/src/telemetry.gen.ts"
mkdir -p "$BUILD_WORKSPACE_DIRECTORY/src/chunkies/testkit/ts/goldens"
install -m 0644 "$here/client/records.json" "$BUILD_WORKSPACE_DIRECTORY/src/chunkies/testkit/ts/goldens/records.json"
echo "refreshed: $prod/{client,sim}.wasm, wum/fixture_park.bin, telemetry.gen.ts, and goldens/records.json"

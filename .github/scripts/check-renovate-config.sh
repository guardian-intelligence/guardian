#!/usr/bin/env bash
# A broken renovate.json5 does not fail the scheduled run — Renovate files a
# config-error issue, skips the repo, and exits 0: a silently dead proposer.
# So this gate fails the PR instead. The validator ships inside the renovate
# distribution, fetched here at the same pinned release the runner in
# renovate.yml uses (the customManagers rule in renovate.json5 moves both).
set -euo pipefail

node_dir="$(cd "$(dirname "$1")" && pwd -P)"
npx="$2"
export npm_config_cache="$TEST_TMPDIR/npm-cache"
PATH="$node_dir:$PATH" \
  "$npx" --yes -p renovate@43.270.0 renovate-config-validator --strict renovate.json5

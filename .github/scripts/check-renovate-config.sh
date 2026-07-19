#!/usr/bin/env bash
# A broken renovate.json5 does not fail the scheduled run — Renovate files a
# config-error issue, skips the repo, and exits 0: a silently dead proposer.
# So this gate fails the PR instead. The validator ships inside the renovate
# distribution, fetched here at the same pinned release the runner in
# renovate.yml uses (the customManagers rule in renovate.json5 moves both).
set -euo pipefail

npx="$1"
export HOME="$TEST_TMPDIR"
"$npx" --yes -p renovate@43.265.1 renovate-config-validator --strict renovate.json5

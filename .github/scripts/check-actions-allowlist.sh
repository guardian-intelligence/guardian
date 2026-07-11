#!/usr/bin/env bash
# The repo runs with `allowed_actions: selected`, so a workflow step using a
# third-party action ref that the GitHub-side allowlist does not carry dies as
# a `startup_failure`: no jobs, no logs, and no in-workflow failure page ever
# fires. This check keeps that failure at PR time instead — every third-party
# `uses:` ref must appear verbatim in .github/actions-allowlist.json, the
# declared source of truth for the GitHub setting.
#
# The setting itself is applied from the file (repo admin required):
#
#   gh api -X PUT repos/<owner>/<repo>/actions/permissions/selected-actions \
#     --input .github/actions-allowlist.json
#
# Bumping a third-party action digest therefore means: update the workflow
# pin AND the allowlist entry in the same PR, then re-apply the setting when
# the PR merges. See docs/dependency-management.md.
set -euo pipefail

repo_root="${1:-.}"
allowlist="${repo_root}/.github/actions-allowlist.json"

mapfile -t allowed < <(python3 -c '
import json, sys
print("\n".join(json.load(open(sys.argv[1]))["patterns_allowed"]))
' "${allowlist}")

failures=0
while IFS=: read -r file _ ref; do
  ref="$(echo "${ref}" | tr -d '"'"'"' ' | sed 's/#.*//')"
  # github-owned actions are covered by github_owned_allowed, local composite
  # actions and docker:// references by neither list.
  case "${ref}" in
  actions/* | github/* | ./* | docker://*) continue ;;
  esac
  hit=false
  for pattern in "${allowed[@]}"; do
    if [[ "${ref}" == "${pattern}" ]]; then
      hit=true
      break
    fi
  done
  if [[ "${hit}" == false ]]; then
    echo "NOT IN ALLOWLIST: ${file}: uses: ${ref}" >&2
    failures=$((failures + 1))
  fi
# -R, not -r: a Bazel runfiles tree presents the workflow files as symlinks,
# which -r silently skips — the check would pass vacuously.
done < <(grep -Rn -E '^\s*-?\s*uses:' "${repo_root}/.github/workflows" | sed -E 's/^([^:]+):([0-9]+):\s*-?\s*uses:\s*/\1:\2:/')

if ((failures > 0)); then
  cat >&2 <<EOF

${failures} action ref(s) missing from .github/actions-allowlist.json.
Add the exact ref(s) to patterns_allowed (drop superseded digests of the
same action), and after merge re-apply the GitHub setting:

  gh api -X PUT repos/\${OWNER}/\${REPO}/actions/permissions/selected-actions \\
    --input .github/actions-allowlist.json

Without the re-apply, workflows using the new digest die as startup_failure
with no logs and no page.
EOF
  exit 1
fi

echo "all third-party action refs are declared in the allowlist"

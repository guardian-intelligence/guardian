#!/usr/bin/env bash
# The repo runs with `allowed_actions: selected`, so a workflow step using a
# third-party action ref that the GitHub-side allowlist does not carry dies as
# a `startup_failure`: no jobs, no logs, and no in-workflow failure page ever
# fires. This check keeps that failure at PR time instead — every third-party
# `uses:` ref must appear verbatim in .github/actions-allowlist.json, the
# declared source of truth for the GitHub setting.
#
# guardian-github/actions.tf imports and reconciles the setting from this
# file. New action refs must be allowed before a workflow uses them: stage
# the allowlist change first and verify its live convergence, then update
# the workflow. Plan-only OpenTofu runs do not apply it. See
# docs/dependency-management.md.
set -euo pipefail

repo_root="${1:-.}"
allowlist="${repo_root}/.github/actions-allowlist.json"

allowed=()
while IFS= read -r pattern; do
  allowed+=("${pattern}")
done < <(python3 -c '
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
done < <(grep -Rn -E '^[[:space:]]*-?[[:space:]]*uses:' "${repo_root}/.github/workflows" | sed -E 's/^([^:]+):([0-9]+):[[:space:]]*-?[[:space:]]*uses:[[:space:]]*/\1:\2:/')

if ((failures > 0)); then
  cat >&2 <<EOF

${failures} action ref(s) missing from .github/actions-allowlist.json.
Add the exact ref(s) to patterns_allowed and reconcile the guardian-github
OpenTofu root before a workflow uses the new digest. A plan-only run is not
convergence. Remove superseded digests only after no workflow needs them.

Without live-setting convergence, workflows using the new digest die as
startup_failure with no logs and no page.
EOF
  exit 1
fi

echo "all third-party action refs are declared in the allowlist"

#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
usage: scripts/release/npm-aisucks-sdk.sh [--publish] [--registry URL]

Build and release @guardian-intelligence/aisucks.

Without --publish, this performs the same package-scoped no-op decision and
prints the local npm pack result. With --publish, it publishes only when the
exact package@version does not already exist on npm.
EOF
}

publish=false
registry="${NPM_CONFIG_REGISTRY:-https://registry.npmjs.org/}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --publish)
      publish=true
      shift
      ;;
    --registry)
      if [[ $# -lt 2 ]]; then
        echo "--registry requires a URL" >&2
        exit 2
      fi
      registry="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

repo_root="$(git rev-parse --show-toplevel)"
workspace_dir="$repo_root/src/viteplus-monorepo"
package_dir="$workspace_dir/packages/aisucks-sdk"

if ! command -v node >/dev/null 2>&1; then
  echo "node is required to read package.json" >&2
  exit 1
fi
if ! command -v npm >/dev/null 2>&1; then
  echo "npm is required to probe and publish the package" >&2
  exit 1
fi

package_name="$(cd "$package_dir" && node -p 'require("./package.json").name')"
package_version="$(cd "$package_dir" && node -p 'require("./package.json").version')"
package_ref="$package_name@$package_version"

if [[ "$package_name" != "@guardian-intelligence/aisucks" ]]; then
  echo "refusing to publish unexpected package name: $package_name" >&2
  exit 1
fi

(cd "$workspace_dir" && node scripts/check-release-hygiene.mjs --package "$package_name")

view_err="$(mktemp)"
pack_dir="$(mktemp -d)"
cleanup() {
  rm -f "$view_err"
  rm -rf "$pack_dir"
}
trap cleanup EXIT

bazelisk build //src/viteplus-monorepo:workspace_build
test -f "$package_dir/dist/index.js"
test -f "$package_dir/dist/index.d.ts"

pack_json="$(cd "$package_dir" && npm pack --json --pack-destination "$pack_dir" --registry "$registry")"
local_integrity="$(printf '%s' "$pack_json" | node -e 'const fs = require("fs"); const data = JSON.parse(fs.readFileSync(0, "utf8")); console.log(data[0].integrity);')"
local_filename="$(printf '%s' "$pack_json" | node -e 'const fs = require("fs"); const data = JSON.parse(fs.readFileSync(0, "utf8")); console.log(data[0].filename);')"

if view_json="$(npm view "$package_ref" dist.integrity --json --registry "$registry" 2>"$view_err")"; then
  published_integrity="$(printf '%s' "$view_json" | node -e 'const fs = require("fs"); const data = JSON.parse(fs.readFileSync(0, "utf8")); console.log(data);')"
  if [[ "$published_integrity" != "$local_integrity" ]]; then
    echo "$package_ref already exists on npm, but HEAD packs different bytes." >&2
    echo "published integrity: $published_integrity" >&2
    echo "local integrity:     $local_integrity" >&2
    echo "Add/apply an SDK Changeset so npm receives a new external version, or restore the package bytes." >&2
    exit 1
  fi
  echo "$package_ref already exists on npm with matching package integrity; no-op."
  if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
    {
      echo "### npm"
      echo
      echo "- Skipped: \`$package_ref\` already exists with matching integrity."
      echo "- Integrity: \`$local_integrity\`"
    } >> "$GITHUB_STEP_SUMMARY"
  fi
  exit 0
fi

if ! grep -q 'E404' "$view_err"; then
  cat "$view_err" >&2
  exit 1
fi

if [[ "$publish" != true ]]; then
  printf '%s\n' "$pack_json"
  echo "$package_ref is publishable; rerun with --publish to publish."
  exit 0
fi

(cd "$package_dir" && npm publish "$pack_dir/$local_filename" --access public --provenance --registry "$registry")

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  {
    echo "### npm"
    echo
    echo "- Published: \`$package_ref\`"
    echo "- Integrity: \`$local_integrity\`"
  } >> "$GITHUB_STEP_SUMMARY"
fi

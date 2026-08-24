#!/usr/bin/env bash
# Runs one workspace package's `vp test run` under Bazel (tools/js/defs.bzl).
#   $1        package directory, repo-relative
#   $2..      repo-relative paths of every workspace file to lay out
# The workspace is rebuilt in a scratch directory from those files, with
# the package's and the root's node_modules linked in, because vite-plus
# wants a writable pnpm workspace and runfiles are neither.
set -euo pipefail
pkg="$1"
shift
root="$TEST_SRCDIR/_main"
node="$root/node"
test -x "$node"
work="$(mktemp -d "${TEST_TMPDIR:-/tmp}/vitest.XXXXXX")"
trap 'rm -rf "$work"' EXIT
for f in "$@"; do
  case "$f" in
    node_modules/*|*/node_modules/*) continue ;;
  esac
  mkdir -p "$work/$(dirname "$f")"
  cp -L "$root/$f" "$work/$f"
done
link_node_modules() {
  mkdir -p "$2"
  find "$1" -mindepth 1 -maxdepth 1 -exec ln -s {} "$2/" \;
}
link_node_modules "$root/node_modules" "$work/node_modules"
link_node_modules "$root/$pkg/node_modules" "$work/$pkg/node_modules"
vp="$work/$pkg/node_modules/vite-plus/bin/vp"
test -x "$vp"
export PATH="$(dirname "$node"):${PATH:-/usr/bin:/bin}"
export NODE_OPTIONS="--preserve-symlinks-main ${NODE_OPTIONS:-}"
export NODE_PATH="$root/node_modules${NODE_PATH:+:$NODE_PATH}"
export CI=true
cd "$work/$pkg"
exec "$node" "$vp" test run --config vitest.config.ts

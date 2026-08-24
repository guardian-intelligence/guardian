#!/usr/bin/env bash
# Runs one workspace package's `vp test run` under Bazel (tools/js/defs.bzl).
#   $1        package directory, repo-relative
#   $2..      repo-relative paths of every workspace file to lay out
# The workspace is rebuilt in a scratch directory from those files, with
# every node_modules the runfiles carry linked beside its package, because
# vite-plus wants a writable pnpm workspace and runfiles are neither.
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

# Link a node_modules directory entry by entry, keeping scopes as real
# directories so their packages are reachable as links below.
link_node_modules() {
  mkdir -p "$2"
  for e in "$1"/* "$1"/.[!.]*; do
    [ -e "$e" ] || continue
    n="$(basename "$e")"
    if [ -d "$e" ] && [ "${n#@}" != "$n" ]; then
      mkdir -p "$2/$n"
      for s in "$e"/*; do
        [ -e "$s" ] && ln -s "$s" "$2/$n/$(basename "$s")"
      done
    else
      ln -s "$e" "$2/$n"
    fi
  done
}
while IFS= read -r nm; do
  link_node_modules "$nm" "$work/${nm#"$root"/}"
done < <(find "$root" -type d -name node_modules -not -path '*/node_modules/*')

# A workspace package link resolves into runfiles (or the source tree);
# point it at the scratch copy instead. vite resolves through real paths,
# and a package's dependencies live beside its copied sources here — not
# beside the checkout, which in CI has no node_modules at all.
srcroot="$(dirname "$(realpath "$root/package.json")")"
while IFS= read -r l; do
  t="$(realpath "$l" 2>/dev/null)" || continue
  case "$t" in
    "$root"/*) rel="${t#"$root"/}" ;;
    "$srcroot"/*) rel="${t#"$srcroot"/}" ;;
    *) continue ;;
  esac
  case "$rel" in node_modules/*|*/node_modules/*) continue ;; esac
  [ -d "$work/$rel" ] && ln -sfn "$work/$rel" "$l"
done < <(find "$work" -type l -path '*/node_modules/*' -not -path '*/node_modules/*/node_modules/*' -not -path '*/node_modules/.*')

vp="$work/$pkg/node_modules/vite-plus/bin/vp"
test -x "$vp"
export PATH="$(dirname "$node"):${PATH:-/usr/bin:/bin}"
export NODE_OPTIONS="--preserve-symlinks-main ${NODE_OPTIONS:-}"
export NODE_PATH="$root/node_modules${NODE_PATH:+:$NODE_PATH}"
export CI=true
cd "$work/$pkg"
exec "$node" "$vp" test run --config vitest.config.ts

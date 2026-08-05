#!/usr/bin/env bash
set -euo pipefail

stage="$(realpath "$1")"
license="$(realpath "$2")"
notices="$(realpath "$3")"
templates="$(dirname "$stage")"
tmp="$(mktemp -d "${TEST_TMPDIR:-/tmp}/pipe-to-remote-box-npm-test.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

grep -q "Apache License" "$license" || fail "product LICENSE is not Apache-2.0 text"
grep -q "strsim 0.11.1" "$notices" || fail "strsim notice is missing"
grep -q "UNICODE LICENSE V3" "$notices" || fail "Unicode notice is missing"

platforms=(
  "x86_64-unknown-linux-musl:linux-x64:linux:x64"
  "aarch64-unknown-linux-musl:linux-arm64:linux:arm64"
  "x86_64-apple-darwin:darwin-x64:darwin:x64"
  "aarch64-apple-darwin:darwin-arm64:darwin:arm64"
)

mkdir -p "$tmp/assets"
for row in "${platforms[@]}"; do
  IFS=: read -r target suffix os cpu <<<"$row"
  manifest="$templates/pipe-to-remote-box-$suffix/package.json"
  [[ -f "$manifest" ]] || fail "missing $suffix package template"
  grep -q '"name": "@guardian-intelligence/pipe-to-remote-box-'"$suffix"'"' "$manifest" \
    || fail "$suffix package has the wrong name"
  grep -q '"version": "0.0.0-dev"' "$manifest" || fail "$suffix template version drifted"
  grep -q '"license": "Apache-2.0"' "$manifest" || fail "$suffix package has the wrong license"
  grep -q '"os": \["'"$os"'"\]' "$manifest" || fail "$suffix package has the wrong OS"
  grep -q '"cpu": \["'"$cpu"'"\]' "$manifest" || fail "$suffix package has the wrong CPU"
  ! grep -q '"scripts"' "$manifest" || fail "$suffix package gained an npm lifecycle script"

  printf '#!/bin/sh\nprintf "pipe-to-remote-box 1.2.3\\n"\n' \
    >"$tmp/assets/pipe-to-remote-box-$target"
  chmod +x "$tmp/assets/pipe-to-remote-box-$target"
done

meta_manifest="$templates/pipe-to-remote-box/package.json"
[[ -f "$meta_manifest" ]] || fail "missing meta package template"
grep -q '"name": "@guardian-intelligence/pipe-to-remote-box"' "$meta_manifest" \
  || fail "meta package has the wrong name"
grep -q '"pipe-to-remote-box": "bin/pipe-to-remote-box"' "$meta_manifest" \
  || fail "meta package does not expose the public binary name"
! grep -q '"scripts"' "$meta_manifest" || fail "meta package gained an npm lifecycle script"

for row in "${platforms[@]}"; do
  IFS=: read -r _ suffix _ _ <<<"$row"
  platform_key="${suffix/-/ }"
  grep -q '"@guardian-intelligence/pipe-to-remote-box-'"$suffix"'": "0.0.0-dev"' "$meta_manifest" \
    || fail "meta package does not pin the $suffix template"
  grep -q '"'"$platform_key"'": "@guardian-intelligence/pipe-to-remote-box-'"$suffix"'"' \
    "$templates/pipe-to-remote-box/bin/pipe-to-remote-box" \
    || fail "shim does not dispatch the $suffix platform"
done

"$stage" 1.2.3 "$tmp/assets" "$tmp/staged" "$license" "$notices"

for package in "$tmp/staged"/*; do
  grep -q '"version": "1.2.3"' "$package/package.json" \
    || fail "$(basename "$package") was not versioned"
  cmp -s "$license" "$package/LICENSE" \
    || fail "$(basename "$package") does not contain the complete product license"
  cmp -s "$notices" "$package/THIRD_PARTY_LICENSES.md" \
    || fail "$(basename "$package") does not contain the complete third-party notices"
done

for row in "${platforms[@]}"; do
  IFS=: read -r target suffix _ _ <<<"$row"
  cmp -s "$tmp/assets/pipe-to-remote-box-$target" \
    "$tmp/staged/pipe-to-remote-box-$suffix/bin/pipe-to-remote-box" \
    || fail "$suffix package does not contain the byte-identical release asset"
  grep -q '"@guardian-intelligence/pipe-to-remote-box-'"$suffix"'": "1.2.3"' \
    "$tmp/staged/pipe-to-remote-box/package.json" \
    || fail "meta package did not pin staged $suffix version"
done

if "$stage" 1.2.3 "$tmp/assets" "$tmp/staged" "$license" "$notices" >/dev/null 2>&1; then
  fail "staging overwrote an existing output directory"
fi
if "$stage" latest "$tmp/assets" "$tmp/invalid" "$license" "$notices" >/dev/null 2>&1; then
  fail "staging accepted a non-version"
fi

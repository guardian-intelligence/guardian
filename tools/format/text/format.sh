#!/usr/bin/env bash
set -euo pipefail

root="$PWD"
while [[ "$root" != "/" && ! -f "$root/MODULE.bazel" ]]; do
  root="${root%/*}"
done
if [[ "$root" == "/" ]]; then
  printf 'format: could not locate repo root from %s\n' "$PWD" >&2
  exit 1
fi

cd "$root"

format_file() {
  local file="$1"
  local tmp
  tmp="$(mktemp "${TMPDIR:-/tmp}/guardian-format.XXXXXX")"
  awk '{ sub(/[[:blank:]]+$/, ""); print }' "$file" >"$tmp"
  # Preserve the tracked file's mode while replacing only its contents.
  command cat "$tmp" >"$file"
  rm -f "$tmp"
}

{
  find .aspect -type f -name '*.axl'
  find .github/workflows -maxdepth 1 -type f -name '*.yml'
  find . -maxdepth 1 -type f \( -name '*.bazel' -o -name '*.md' -o -name '*.yaml' -o -name '*.yml' -o -name '*.json' \)
  find docs -type f \( -name '*.md' -o -name '*.yaml' \)
  find src/infrastructure -type f \( -name '*.json' -o -name '*.tf' -o -name '*.yaml' \)
  find tools -type f \( -name '*.bazel' -o -name '*.bzl' \)
} | LC_ALL=C sort -u | while IFS= read -r file; do
  format_file "$file"
done

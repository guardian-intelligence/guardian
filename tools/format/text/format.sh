#!/usr/bin/env bash
set -euo pipefail
shopt -s globstar nullglob extglob

root="$PWD"
while [[ "$root" != "/" && ! -f "$root/MODULE.bazel" ]]; do
  root="${root%/*}"
done
if [[ "$root" == "/" ]]; then
  printf 'format: could not locate repo root from %s\n' "$PWD" >&2
  exit 1
fi

cd "$root"

patterns=(
  ".aspect/**/*.axl"
  ".github/workflows/*.yml"
  "*.bazel"
  "*.md"
  "*.yaml"
  "*.yml"
  "*.json"
  "docs/**/*.md"
  "docs/**/*.yaml"
  "src/infrastructure/**/*.json"
  "src/infrastructure/**/*.tf"
  "src/infrastructure/**/*.yaml"
  "tools/**/*.bazel"
  "tools/**/*.bzl"
)

format_file() {
  local file="$1"
  local line
  local lines=()

  mapfile -t lines < "$file"
  : > "$file"
  for line in "${lines[@]}"; do
    line="${line%%+([[:blank:]])}"
    printf '%s\n' "$line" >> "$file"
  done
}

seen=" "
for pattern in "${patterns[@]}"; do
  for file in $pattern; do
    [[ -f "$file" ]] || continue
    case "$seen" in
      *" $file "*) continue ;;
    esac
    seen="$seen$file "
    format_file "$file"
  done
done

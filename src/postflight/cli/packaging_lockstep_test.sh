#!/usr/bin/env bash
# Three files independently name and version this binary: Cargo.toml (what
# cargo and crates.io build), the Makefile (the hand-install path), and
# BUILD.bazel (the target the release lane ships). A version bump touches
# Cargo.toml alone, so without this test the Bazel binary keeps reporting the
# old version while every other check stays green.
set -euo pipefail

cargo_file="$1"
build_file="$2"
make_file="$3"

toml_value() {
  local section="$1" key="$2" file="$3"
  awk -v section="$section" -v key="$key" '
    /^[[:space:]]*\[/ {
      current = $0
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", current)
      next
    }
    current == section && $0 ~ "^[[:space:]]*" key "[[:space:]]*=" {
      sub(/^[^=]*=[[:space:]]*"/, "")
      sub(/".*$/, "")
      print
      exit
    }
  ' "$file"
}

cargo_bin="$(toml_value '[[bin]]' name "$cargo_file")"
cargo_version="$(toml_value '[package]' version "$cargo_file")"
make_bin="$(sed -n 's/^BIN[[:space:]]*=[[:space:]]*\([^[:space:]]*\).*/\1/p' "$make_file" | head -n 1)"
bazel_bin="$(awk '/^rust_binary\(/ { found = 1 } found && /name = "/ { sub(/^[^"]*"/, ""); sub(/".*$/, ""); print; exit }' "$build_file")"
bazel_version="$(sed -n 's/.*"CARGO_PKG_VERSION":[[:space:]]*"\([^"]*\)".*/\1/p' "$build_file" | head -n 1)"

for pair in "cargo_bin:$cargo_bin" "cargo_version:$cargo_version" "make_bin:$make_bin" \
  "bazel_bin:$bazel_bin" "bazel_version:$bazel_version"; do
  if [[ -z "${pair#*:}" ]]; then
    echo "failed to extract ${pair%%:*}" >&2
    exit 1
  fi
done

status=0

if [[ "$cargo_bin" != "$make_bin" || "$cargo_bin" != "$bazel_bin" ]]; then
  echo "binary name drift: Cargo.toml [[bin]] is '$cargo_bin', Makefile BIN is '$make_bin', rust_binary target is '$bazel_bin'" >&2
  status=1
fi

if [[ "$cargo_version" != "$bazel_version" ]]; then
  echo "version drift: Cargo.toml pins $cargo_version but BUILD.bazel stamps CARGO_PKG_VERSION=$bazel_version" >&2
  status=1
fi

exit "$status"

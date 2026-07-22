#!/usr/bin/env bash
# The Bazel toolchain (rust.MODULE.bazel) and the cargo/rustup toolchain
# (rust-toolchain.toml, used by the edge release lane) must pin the same Rust
# version, or CI and released binaries silently build with different compilers.
set -euo pipefail

module_file="$1"
toolchain_file="$2"

module_version="$(grep -o 'versions = \["[0-9][0-9.]*"\]' "$module_file" | grep -o '[0-9][0-9.]*')"
toolchain_version="$(grep -o 'channel = "[0-9][0-9.]*"' "$toolchain_file" | grep -o '[0-9][0-9.]*')"

if [[ -z "$module_version" || -z "$toolchain_version" ]]; then
  echo "failed to extract a pinned version (module='$module_version' toolchain='$toolchain_version')" >&2
  exit 1
fi

if [[ "$module_version" != "$toolchain_version" ]]; then
  echo "Rust version drift: rust.MODULE.bazel pins $module_version but rust-toolchain.toml pins $toolchain_version" >&2
  exit 1
fi

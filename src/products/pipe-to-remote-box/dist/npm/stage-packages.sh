#!/usr/bin/env bash
# Assemble npm packages from templates after the caller has verified the
# release checksums and Sigstore bundles. This script performs no download and
# no publication.
set -euo pipefail

if [[ "$#" -ne 5 ]]; then
  echo "usage: stage-packages.sh <version> <verified-assets-dir> <output-dir> <license> <third-party-notices>" >&2
  exit 2
fi

version="$1"
assets_dir="$2"
output_dir="$3"
license_file="$4"
notices_file="$5"
template_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "version must be a bare stable semantic version, got '$version'" >&2
  exit 2
fi
[[ -d "$assets_dir" ]] || { echo "verified asset directory does not exist: $assets_dir" >&2; exit 1; }
[[ -f "$license_file" ]] || { echo "license file does not exist: $license_file" >&2; exit 1; }
[[ -f "$notices_file" ]] || { echo "third-party notices do not exist: $notices_file" >&2; exit 1; }
[[ ! -e "$output_dir" ]] || { echo "output directory already exists: $output_dir" >&2; exit 1; }

mkdir -p -- "$output_dir"

platforms=(
  "x86_64-unknown-linux-musl:linux-x64"
  "aarch64-unknown-linux-musl:linux-arm64"
  "x86_64-apple-darwin:darwin-x64"
  "aarch64-apple-darwin:darwin-arm64"
)

rewrite_version() {
  local manifest="$1" temporary="$1.tmp"
  sed "s/0\.0\.0-dev/$version/g" "$manifest" >"$temporary"
  mv -- "$temporary" "$manifest"
}

for pair in "${platforms[@]}"; do
  target="${pair%%:*}"
  suffix="${pair#*:}"
  package="pipe-to-remote-box-$suffix"
  asset="$assets_dir/pipe-to-remote-box-$target"
  destination="$output_dir/$package"

  [[ -f "$asset" ]] || { echo "verified release asset is missing: $asset" >&2; exit 1; }
  cp -R -- "$template_dir/$package" "$destination"
  mkdir -p -- "$destination/bin"
  install -m 0755 "$asset" "$destination/bin/pipe-to-remote-box"
  install -m 0644 "$license_file" "$destination/LICENSE"
  install -m 0644 "$notices_file" "$destination/THIRD_PARTY_LICENSES.md"
  rewrite_version "$destination/package.json"
done

meta="$output_dir/pipe-to-remote-box"
cp -R -- "$template_dir/pipe-to-remote-box" "$meta"
install -m 0644 "$license_file" "$meta/LICENSE"
install -m 0644 "$notices_file" "$meta/THIRD_PARTY_LICENSES.md"
rewrite_version "$meta/package.json"

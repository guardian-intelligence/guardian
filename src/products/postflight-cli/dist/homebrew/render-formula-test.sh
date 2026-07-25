#!/usr/bin/env bash
set -euo pipefail

renderer="$1"
template="$2"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

mapfile -t targets < <(
  grep -oE 'postflight-cli%2Fv@VERSION@/postflight-[A-Za-z0-9_.-]+' "$template" |
    sed 's|.*/postflight-||' |
    sort -u
)

if ((${#targets[@]} == 0)); then
  echo "the template declares no release binaries" >&2
  exit 1
fi

checksums="$workdir/checksums.txt"
: >"$checksums"
index=0
for target in "${targets[@]}"; do
  printf '%064d  postflight-%s\n' "$index" "$target" >>"$checksums"
  index=$((index + 1))
done
printf '%064d  checksums-of-an-unrelated-asset\n' 999 >>"$checksums"

formula="$workdir/postflight.rb"
"$renderer" "$template" 9.8.7 "$checksums" >"$formula"

if ! grep -q 'version "9.8.7"' "$formula"; then
  echo "the rendered formula does not carry the version it was rendered for" >&2
  exit 1
fi

if grep -nE '@[A-Z0-9_]+@' "$formula"; then
  echo "the rendered formula still carries placeholders" >&2
  exit 1
fi

index=0
for target in "${targets[@]}"; do
  expected="$(printf '%064d' "$index")"
  if ! grep -q "sha256 \"$expected\"" "$formula"; then
    echo "the rendered formula does not carry the checksum for postflight-$target" >&2
    exit 1
  fi
  if ! grep -q "postflight-cli%2Fv9.8.7/postflight-$target\"" "$formula"; then
    echo "the rendered formula does not download postflight-$target from the release" >&2
    exit 1
  fi
  index=$((index + 1))
done

# A release missing one of the binaries the formula promises must fail the
# render, never publish a formula that 404s for that platform's users.
short="$workdir/short-checksums.txt"
grep -v " postflight-${targets[0]}\$" "$checksums" >"$short"
if "$renderer" "$template" 9.8.7 "$short" >/dev/null 2>&1; then
  echo "the renderer accepted a checksums.txt missing postflight-${targets[0]}" >&2
  exit 1
fi

echo "render-formula renders ${#targets[@]} targets and rejects an incomplete release"

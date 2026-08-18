#!/usr/bin/env bash
set -euo pipefail

renderer="$1"
template="$2"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

targets=()
while IFS= read -r target; do
  targets+=("${target}")
done < <(
  grep -oE 'postflight-cli%2Fv@VERSION@/postflight-[A-Za-z0-9_.-]+' "$template" |
    sed 's|.*/postflight-||' |
    sort -u
)

if ((${#targets[@]} == 0)); then
  echo "the template declares no release binaries" >&2
  exit 1
fi

version=9.8.7

checksums="$workdir/checksums.txt"
: >"$checksums"
expected="$workdir/expected-pairs.txt"
: >"$expected"
index=0
for target in "${targets[@]}"; do
  printf '%064d  postflight-%s\n' "$index" "$target" >>"$checksums"
  printf '%s %064d\n' "$target" "$index" >>"$expected"
  index=$((index + 1))
done
printf '%064d  checksums-of-an-unrelated-asset\n' 999 >>"$checksums"

formula="$workdir/postflight.rb"
"$renderer" "$template" "$version" "$checksums" >"$formula"

if ! grep -q "version \"$version\"" "$formula"; then
  echo "the rendered formula does not carry the version it was rendered for" >&2
  exit 1
fi

if grep -nE '@[A-Z0-9_]+@' "$formula"; then
  echo "the rendered formula still carries placeholders" >&2
  exit 1
fi

# brew verifies the checksum of the file it just downloaded, so a formula whose
# sha256 lines are each individually correct but attached to the wrong url fails
# `brew install` on every platform it mismatches. Read every url back together
# with the sha256 that guards it.
rendered="$workdir/rendered-pairs.txt"
awk -v prefix="/postflight-cli%2Fv$version/postflight-" '
  {
    at = index($0, prefix)
    if (at > 0) {
      pending = substr($0, at + length(prefix))
      sub(/".*/, "", pending)
      next
    }
    if (match($0, /sha256 "[0-9a-f]+"/)) {
      print (pending == "" ? "unguarded-checksum" : pending), substr($0, RSTART + 8, RLENGTH - 9)
      pending = ""
    }
  }
' "$formula" | sort >"$rendered"

if ! diff -u <(sort "$expected") "$rendered" >"$workdir/pairs.diff"; then
  echo "the rendered formula guards its downloads with the wrong checksums:" >&2
  cat "$workdir/pairs.diff" >&2
  exit 1
fi

# A release missing one of the binaries the formula promises must fail the
# render, never publish a formula that 404s for that platform's users.
short="$workdir/short-checksums.txt"
grep -v " postflight-${targets[0]}\$" "$checksums" >"$short"
if "$renderer" "$template" "$version" "$short" >/dev/null 2>&1; then
  echo "the renderer accepted a checksums.txt missing postflight-${targets[0]}" >&2
  exit 1
fi

echo "render-formula renders ${#targets[@]} targets and rejects an incomplete release"

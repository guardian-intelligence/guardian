#!/usr/bin/env bash
# Renders the Homebrew binary formula for a stable release from the template
# and that release's checksums.txt, writing it to stdout. The tap repository
# receives only this output; the formula's source of truth stays here.
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: render-formula.sh <template> <version> <checksums.txt>" >&2
  exit 2
fi

template="$1"
version="$2"
checksums="$3"

# The template's download URLs are the target list, so the renderer never
# carries a second copy of it.
targets=()
while IFS= read -r target; do
  targets+=("${target}")
done < <(
  grep -oE 'postflight-cli%2Fv@VERSION@/postflight-[A-Za-z0-9_.-]+' "$template" |
    sed 's|.*/postflight-||' |
    sort -u
)

if ((${#targets[@]} == 0)); then
  echo "$template declares no release binaries to render" >&2
  exit 1
fi

formula="$(sed "s/@VERSION@/$version/g" "$template")"

for target in "${targets[@]}"; do
  sum="$(awk -v f="postflight-$target" '$2 == f { print $1 }' "$checksums")"
  if [[ -z "$sum" ]]; then
    echo "$checksums carries no entry for postflight-$target" >&2
    exit 1
  fi
  placeholder="@SHA256_$(echo "$target" | tr 'a-z-' 'A-Z_')@"
  formula="${formula//"$placeholder"/$sum}"
done

if grep -qE '@[A-Z0-9_]+@' <<<"$formula"; then
  echo "the rendered formula still carries placeholders:" >&2
  grep -nE '@[A-Z0-9_]+@' <<<"$formula" >&2
  exit 1
fi

printf '%s\n' "$formula"

#!/usr/bin/env bash
# Source gate for the fixed-point invariant: no float types anywhere in the
# sim workspace, comments included - the token itself is banned so the
# invariant can't erode through "temporary" exceptions.
set -euo pipefail

fail=0
for f in "$@"; do
  if grep -nE '\bf(32|64)\b' "$f"; then
    echo "float type token in $f - the sim core is fixed-point only (Q16.16)" >&2
    fail=1
  fi
done
exit $fail

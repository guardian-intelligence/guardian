#!/usr/bin/env bash
set -euo pipefail

oathtool="${1:?usage: oathtool_test.sh <oathtool>}"

version="$("${oathtool}" --version | head -1)"
[[ "${version}" =~ ^oathtool\ \(OATH\ Toolkit\)\ [0-9]+\.[0-9]+\.[0-9]+$ ]]

code="$(
  "${oathtool}" \
    --base32 \
    --digits=8 \
    --now=@59 \
    --totp=sha1 \
    GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ
)"
[[ "${code}" == "94287082" ]]

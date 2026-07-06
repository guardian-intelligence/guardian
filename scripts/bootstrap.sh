#!/usr/bin/env bash
# Detect OS/arch and exec the matching scripts/bootstrap/bootstrap-<os>-<arch>
# script, forwarding all arguments. This is the one command every developer,
# agent, and CI environment runs to get bazelisk + aspect on PATH:
#
#   eval "$(scripts/bootstrap.sh path)"
#
# See scripts/bootstrap/bootstrap-<os>-<arch> for what actually gets
# installed and why - this file is purely a platform dispatcher.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

os="$(uname -s)"
arch="$(uname -m)"

case "${os}-${arch}" in
Linux-x86_64)
  target="${repo_root}/bootstrap/bootstrap-linux-amd64"
  ;;
Darwin-arm64)
  target="${repo_root}/bootstrap/bootstrap-darwin-arm64"
  ;;
*)
  echo "unsupported bootstrap platform: ${os} ${arch}" >&2
  echo "scripts/bootstrap/ only pins Linux x86_64 and Darwin arm64 artifacts." >&2
  exit 1
  ;;
esac

exec "${target}" "$@"

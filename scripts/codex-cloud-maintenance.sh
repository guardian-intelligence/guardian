#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export KUBECONFIG="${HOME}/.kube/guardian-codex-cloud"
tool_bin_dir="${GUARDIAN_CODEX_TOOL_BIN_DIR:-/usr/local/bin}"

eval "$("${repo_root}/scripts/bootstrap.sh" path)"
aspect tools install-codex-cloud --bin-dir "${tool_bin_dir}"
export PATH="${tool_bin_dir}:${PATH}"
"${repo_root}/tools/ops/codex-cloud-tunnel" start
kubectl --request-timeout=15s auth whoami >/dev/null
echo "Guardian cluster read path is ready."

#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export KUBECONFIG="${HOME}/.kube/guardian-codex-cloud"

eval "$("${repo_root}/scripts/bootstrap.sh" path)"
aspect tools install-codex-cloud
eval "$(aspect tools path)"
"${repo_root}/tools/ops/codex-cloud-tunnel" start
kubectl --request-timeout=15s auth whoami >/dev/null
echo "Guardian cluster read path is ready."

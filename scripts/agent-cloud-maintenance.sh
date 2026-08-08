#!/usr/bin/env bash
set -euo pipefail

provider="${GUARDIAN_AGENT_PROVIDER:-}"
case "${provider}" in
cursor | devin) ;;
*)
  echo "GUARDIAN_AGENT_PROVIDER must be cursor or devin" >&2
  exit 2
  ;;
esac

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export KUBECONFIG="${HOME}/.kube/guardian-${provider}-cloud"
export PATH="${HOME}/.local/bin:${PATH}"

eval "$("${repo_root}/scripts/bootstrap.sh" path)"
aspect tools install-agent-cloud --bin-dir "${repo_root}/.guardian/tools/bin"
mkdir -p "${HOME}/.local/bin"
for tool in cloudflared kubectl; do
  ln -sfn "${repo_root}/.guardian/tools/bin/${tool}" "${HOME}/.local/bin/${tool}"
done
"${repo_root}/tools/ops/agent-cloud-tunnel" start
kubectl --request-timeout=15s auth whoami >/dev/null || {
  echo "the JIT cluster credential expired; mint and inject a new session credential" >&2
  exit 1
}
echo "Guardian ${provider} delivery-read path is ready."

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

: "${GUARDIAN_AGENT_KUBERNETES_TOKEN:?set as a session secret}"
: "${GUARDIAN_AGENT_KUBERNETES_CA_B64:?set as an environment variable}"
: "${GUARDIAN_AGENT_CF_ACCESS_CLIENT_ID:?set as a provider secret}"
: "${GUARDIAN_AGENT_CF_ACCESS_CLIENT_SECRET:?set as a provider secret}"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kube_dir="${HOME}/.kube"
config_dir="${HOME}/.config/guardian"
kubeconfig="${kube_dir}/guardian-${provider}-cloud"
default_kubeconfig="${kube_dir}/config"
ca_file="${config_dir}/guardian-mgmt-ca.crt"
tool_bin_dir="${GUARDIAN_AGENT_TOOL_BIN_DIR:-${HOME}/.local/bin}"

# Materialize the four inputs first, then remove them from the environment
# before bootstrap executes any repository-controlled build/tool code.
umask 077
mkdir -p "${kube_dir}" "${config_dir}" "${tool_bin_dir}"
printf '%s' "${GUARDIAN_AGENT_KUBERNETES_CA_B64}" | base64 --decode >"${ca_file}"
printf 'TUNNEL_SERVICE_TOKEN_ID=%q\nTUNNEL_SERVICE_TOKEN_SECRET=%q\n' \
  "${GUARDIAN_AGENT_CF_ACCESS_CLIENT_ID}" \
  "${GUARDIAN_AGENT_CF_ACCESS_CLIENT_SECRET}" \
  >"${config_dir}/${provider}-cloud-tunnel.env"
cat >"${kubeconfig}" <<EOF
apiVersion: v1
kind: Config
clusters:
  - name: guardian-mgmt
    cluster:
      server: https://127.0.0.1:16443
      tls-server-name: k8s.guardianintelligence.org
      certificate-authority: ${ca_file}
contexts:
  - name: guardian-${provider}-cloud
    context:
      cluster: guardian-mgmt
      user: guardian-${provider}-cloud
current-context: guardian-${provider}-cloud
users:
  - name: guardian-${provider}-cloud
    user:
      token: ${GUARDIAN_AGENT_KUBERNETES_TOKEN}
EOF
chmod 600 "${ca_file}" "${config_dir}/${provider}-cloud-tunnel.env" "${kubeconfig}"
unset GUARDIAN_AGENT_KUBERNETES_TOKEN GUARDIAN_AGENT_KUBERNETES_CA_B64
unset GUARDIAN_AGENT_CF_ACCESS_CLIENT_ID GUARDIAN_AGENT_CF_ACCESS_CLIENT_SECRET

# Provider VMs are dedicated to this checkout. Install the named config as the
# default only when doing so cannot overwrite another cluster identity; this
# makes Cursor's start command useful to every later agent shell.
if [[ ! -e "${default_kubeconfig}" && ! -L "${default_kubeconfig}" ]]; then
  ln -s "${kubeconfig}" "${default_kubeconfig}"
elif [[ ! -L "${default_kubeconfig}" || "$(readlink "${default_kubeconfig}")" != "${kubeconfig}" ]]; then
  echo "refusing to replace existing default kubeconfig ${default_kubeconfig}" >&2
  exit 1
fi

eval "$("${repo_root}/scripts/bootstrap.sh" path)"
aspect tools install-agent-cloud --bin-dir "${repo_root}/.guardian/tools/bin"
for tool in cloudflared kubectl; do
  ln -sfn "${repo_root}/.guardian/tools/bin/${tool}" "${tool_bin_dir}/${tool}"
done
for tool in aspect bazelisk; do
  ln -sfn "$(command -v "${tool}")" "${tool_bin_dir}/${tool}"
done
export PATH="${tool_bin_dir}:${PATH}"
export KUBECONFIG="${kubeconfig}"
export GUARDIAN_AGENT_PROVIDER="${provider}"
"${repo_root}/tools/ops/agent-cloud-tunnel" start

expected="system:serviceaccount:tenant-root:guardian-cloud-agent-${provider}"
actual="$(kubectl --request-timeout=15s auth whoami -o jsonpath='{.status.userInfo.username}')"
if [[ "${actual}" != "${expected}" ]]; then
  echo "unexpected Kubernetes identity ${actual}; expected ${expected}" >&2
  exit 1
fi
if [[ "${provider}" == "cursor" ]]; then
  echo "Guardian cursor platform-read path is ready."
else
  echo "Guardian devin delivery-read path is ready."
fi

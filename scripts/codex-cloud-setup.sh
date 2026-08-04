#!/usr/bin/env bash
set -euo pipefail

: "${GUARDIAN_CODEX_KUBECONFIG_B64:?set as a Codex environment secret}"
: "${GUARDIAN_CODEX_OIDC_CACHE_TGZ_B64:?set as a Codex environment secret}"
: "${CF_ACCESS_CLIENT_ID:?set as a Codex environment secret}"
: "${CF_ACCESS_CLIENT_SECRET:?set as a Codex environment secret}"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kube_dir="${HOME}/.kube"
oidc_cache_dir="${kube_dir}/cache/oidc-login-platform-agent"
config_dir="${HOME}/.config/guardian"
kubeconfig="${kube_dir}/guardian-codex-cloud"

umask 077
eval "$("${repo_root}/scripts/bootstrap.sh" path)"
aspect tools install
eval "$(aspect tools path)"
mkdir -p "${oidc_cache_dir}" "${config_dir}"
printf '%s' "${GUARDIAN_CODEX_KUBECONFIG_B64}" | base64 --decode >"${kubeconfig}.encoded"
sed \
  -e "s#__GUARDIAN_HOME__#${HOME}#g" \
  -e "s#__GUARDIAN_REPO_ROOT__#${repo_root}#g" \
  "${kubeconfig}.encoded" >"${kubeconfig}"
rm -f "${kubeconfig}.encoded"
printf '%s' "${GUARDIAN_CODEX_OIDC_CACHE_TGZ_B64}" | base64 --decode | tar -xzf - -C "${oidc_cache_dir}"
printf 'TUNNEL_SERVICE_TOKEN_ID=%q\nTUNNEL_SERVICE_TOKEN_SECRET=%q\n' \
  "${CF_ACCESS_CLIENT_ID}" "${CF_ACCESS_CLIENT_SECRET}" \
  >"${config_dir}/codex-cloud-tunnel.env"
chmod 600 "${kubeconfig}" "${config_dir}/codex-cloud-tunnel.env"

profile="${HOME}/.bashrc"
profile_marker="# Guardian Codex cloud cluster access"
if ! grep -Fq "${profile_marker}" "${profile}" 2>/dev/null; then
  {
    printf '\n%s\n' "${profile_marker}"
    printf 'export KUBECONFIG=%q\n' "${kubeconfig}"
    printf 'export PATH=%q:$PATH\n' "${repo_root}/.guardian/tools/bin"
  } >>"${profile}"
fi

export KUBECONFIG="${kubeconfig}"
"${repo_root}/tools/ops/codex-cloud-tunnel" start
kubectl --request-timeout=15s auth whoami >/dev/null
echo "Guardian cluster read path is ready."

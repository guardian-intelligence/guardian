#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: seed-openbao-evidence-token [--kubeconfig PATH] [--context NAME]

Seeds Secret/tenant-root/guardian-openbao-evidence-token from environment
variables. Secret values are written to kubectl stdin, not command-line
arguments.

Normally run through:
  aspect infra seed-openbao-evidence-token --kubeconfig "${KUBECONFIG}"

Inputs:
  KUBECTL_BIN                       repo-pinned kubectl executable path
  GUARDIAN_OPENBAO_EVIDENCE_TOKEN   preferred evidence token
                                    or OPENBAO_TOKEN, BAO_TOKEN, VAULT_TOKEN
EOF
}

kubeconfig=
context=
while [[ $# -gt 0 ]]; do
  case "$1" in
    --kubeconfig)
      kubeconfig="${2:?--kubeconfig requires a value}"
      shift 2
      ;;
    --context)
      context="${2:?--context requires a value}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

first_nonempty() {
  local value
  for value in "$@"; do
    if [[ -n "${value}" ]]; then
      printf '%s' "${value}"
      return 0
    fi
  done
  return 1
}

kubectl_bin="${KUBECTL_BIN:-}"
token="$(first_nonempty "${GUARDIAN_OPENBAO_EVIDENCE_TOKEN:-}" "${OPENBAO_TOKEN:-}" "${BAO_TOKEN:-}" "${VAULT_TOKEN:-}" || true)"

missing=()
[[ -n "${kubectl_bin}" ]] || missing+=("KUBECTL_BIN")
[[ -n "${token}" ]] || missing+=("GUARDIAN_OPENBAO_EVIDENCE_TOKEN or OPENBAO_TOKEN or BAO_TOKEN or VAULT_TOKEN")

if [[ "${#missing[@]}" -gt 0 ]]; then
  echo "missing required secret inputs:" >&2
  printf '  - %s\n' "${missing[@]}" >&2
  exit 1
fi

kubectl_args=()
if [[ -n "${kubeconfig}" ]]; then
  kubectl_args+=(--kubeconfig "${kubeconfig}")
fi
if [[ -n "${context}" ]]; then
  kubectl_args+=(--context "${context}")
fi

emit_block_scalar() {
  local key="$1"
  local value="$2"

  printf '  %s: |-\n' "${key}"
  while IFS= read -r line || [[ -n "${line}" ]]; do
    printf '    %s\n' "${line}"
  done <<<"${value}"
}

{
  cat <<'EOF'
apiVersion: v1
kind: Secret
metadata:
  name: guardian-openbao-evidence-token
  namespace: tenant-root
  labels:
    guardian.dev/secret-contract: openbao-evidence-token
type: Opaque
stringData:
EOF
  emit_block_scalar "token" "${token}"
} | "${kubectl_bin}" "${kubectl_args[@]}" apply -f -

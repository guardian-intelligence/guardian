#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: seed-db-backup-secret [--kubeconfig PATH] [--context NAME]

Seeds Secret/tenant-root/guardian-r2-db-backups from environment variables.
Secret values are written to kubectl stdin, not command-line arguments.

Normally run through:
  aspect infra seed-db-backup-secret --kubeconfig "${KUBECONFIG}"

Inputs:
  KUBECTL_BIN                           repo-pinned kubectl executable path
  GUARDIAN_R2_BACKUP_BUCKET              default: guardian-vault
  GUARDIAN_R2_BACKUP_ENDPOINT            or cloudflare_r2_s3_api_endpoint
  GUARDIAN_R2_BACKUP_REGION              default: auto
  GUARDIAN_R2_BACKUP_ACCESS_KEY_ID       or AWS_ACCESS_KEY_ID or cloudflare_r2_access_key_id
  GUARDIAN_R2_BACKUP_SECRET_ACCESS_KEY   or AWS_SECRET_ACCESS_KEY or cloudflare_r2_secret_access_key
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

bucket="${GUARDIAN_R2_BACKUP_BUCKET:-guardian-vault}"
endpoint="$(first_nonempty "${GUARDIAN_R2_BACKUP_ENDPOINT:-}" "${cloudflare_r2_s3_api_endpoint:-}" || true)"
region="${GUARDIAN_R2_BACKUP_REGION:-auto}"
access_key_id="$(first_nonempty "${GUARDIAN_R2_BACKUP_ACCESS_KEY_ID:-}" "${AWS_ACCESS_KEY_ID:-}" "${cloudflare_r2_access_key_id:-}" || true)"
secret_access_key="$(first_nonempty "${GUARDIAN_R2_BACKUP_SECRET_ACCESS_KEY:-}" "${AWS_SECRET_ACCESS_KEY:-}" "${cloudflare_r2_secret_access_key:-}" || true)"
kubectl_bin="${KUBECTL_BIN:-}"

missing=()
[[ -n "${kubectl_bin}" ]] || missing+=("KUBECTL_BIN")
[[ -n "${bucket}" ]] || missing+=("GUARDIAN_R2_BACKUP_BUCKET")
[[ -n "${endpoint}" ]] || missing+=("GUARDIAN_R2_BACKUP_ENDPOINT or cloudflare_r2_s3_api_endpoint")
[[ -n "${region}" ]] || missing+=("GUARDIAN_R2_BACKUP_REGION")
[[ -n "${access_key_id}" ]] || missing+=("GUARDIAN_R2_BACKUP_ACCESS_KEY_ID or AWS_ACCESS_KEY_ID or cloudflare_r2_access_key_id")
[[ -n "${secret_access_key}" ]] || missing+=("GUARDIAN_R2_BACKUP_SECRET_ACCESS_KEY or AWS_SECRET_ACCESS_KEY or cloudflare_r2_secret_access_key")

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
  name: guardian-r2-db-backups
  namespace: tenant-root
  labels:
    guardian.dev/secret-contract: r2-db-backups
type: Opaque
stringData:
EOF
  emit_block_scalar "bucketName" "${bucket}"
  emit_block_scalar "endpoint" "${endpoint}"
  emit_block_scalar "region" "${region}"
  emit_block_scalar "AWS_ACCESS_KEY_ID" "${access_key_id}"
  emit_block_scalar "AWS_SECRET_ACCESS_KEY" "${secret_access_key}"
} | "${kubectl_bin}" "${kubectl_args[@]}" apply -f -

#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: capture-management-evidence [OPTIONS]

Captures live management-cluster evidence into a report directory under the
repo. The script is read-only against Kubernetes and Talos; run the evidence
apply/wait tasks before this command when collecting load/restore output.

Options:
  --out-dir DIR           output directory; defaults to
                          docs/reports/infrastructure/live-runs/<timestamp>-<phase>
  --phase NAME            label for this capture phase, e.g. baseline,
                          evidence, outage-before, outage-down, outage-after
  --kubeconfig PATH       optional kubeconfig path
  --context NAME          optional kubeconfig context
  --talosconfig PATH      optional talosconfig path; Talos capture is skipped
                          unless this option or TALOSCONFIG is set
  --talos-endpoints LIST  Talos endpoint list
  --talos-nodes LIST      Talos node list
  --api-server URL        Kubernetes API VIP URL; default https://10.8.0.250:6443
  --api-load-requests N   API VIP read count; default 60
  --api-load-concurrency N API VIP concurrent workers; default 6
  --allow-failures        write all outputs and exit 0 even if a command fails
  -h, --help              show this help

Inputs:
  KUBECTL_BIN             repo-pinned kubectl executable path
  TALOSCTL_BIN            repo-pinned talosctl executable path
EOF
}

phase="snapshot"
out_dir=""
kubeconfig=""
context=""
talosconfig="${TALOSCONFIG:-}"
talos_endpoints="10.8.0.250"
talos_nodes="10.8.0.11,10.8.0.12,10.8.0.13"
api_server="https://10.8.0.250:6443"
api_load_requests="60"
api_load_concurrency="6"
allow_failures=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --out-dir)
      out_dir="${2:?--out-dir requires a value}"
      shift 2
      ;;
    --phase)
      phase="${2:?--phase requires a value}"
      shift 2
      ;;
    --kubeconfig)
      kubeconfig="${2:?--kubeconfig requires a value}"
      shift 2
      ;;
    --context)
      context="${2:?--context requires a value}"
      shift 2
      ;;
    --talosconfig)
      talosconfig="${2:?--talosconfig requires a value}"
      shift 2
      ;;
    --talos-endpoints)
      talos_endpoints="${2:?--talos-endpoints requires a value}"
      shift 2
      ;;
    --talos-nodes)
      talos_nodes="${2:?--talos-nodes requires a value}"
      shift 2
      ;;
    --api-server)
      api_server="${2:?--api-server requires a value}"
      shift 2
      ;;
    --api-load-requests)
      api_load_requests="${2:?--api-load-requests requires a value}"
      shift 2
      ;;
    --api-load-concurrency)
      api_load_concurrency="${2:?--api-load-concurrency requires a value}"
      shift 2
      ;;
    --allow-failures)
      allow_failures=true
      shift
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

kubectl_bin="${KUBECTL_BIN:-}"
talosctl_bin="${TALOSCTL_BIN:-}"
missing=()
[[ -n "${kubectl_bin}" ]] || missing+=("KUBECTL_BIN")
[[ -x "${kubectl_bin}" ]] || missing+=("executable KUBECTL_BIN")
[[ -n "${talosctl_bin}" ]] || missing+=("TALOSCTL_BIN")
[[ -x "${talosctl_bin}" ]] || missing+=("executable TALOSCTL_BIN")
if [[ ! "${api_load_requests}" =~ ^[1-9][0-9]*$ ]]; then
  missing+=("positive integer --api-load-requests")
fi
if [[ ! "${api_load_concurrency}" =~ ^[1-9][0-9]*$ ]]; then
  missing+=("positive integer --api-load-concurrency")
fi
if [[ "${#missing[@]}" -gt 0 ]]; then
  echo "missing required inputs:" >&2
  printf '  - %s\n' "${missing[@]}" >&2
  exit 1
fi

safe_phase="$(printf '%s' "${phase}" | tr -c 'A-Za-z0-9_.-' '-')"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
if [[ -z "${out_dir}" ]]; then
  out_dir="docs/reports/infrastructure/live-runs/${timestamp}-${safe_phase}"
fi

mkdir -p "${out_dir}/kubectl" "${out_dir}/talos" "${out_dir}/evidence"
summary="${out_dir}/summary.tsv"
: >"${summary}"
failures=0

kubectl_args=()
if [[ -n "${kubeconfig}" ]]; then
  kubectl_args+=(--kubeconfig "${kubeconfig}")
fi
if [[ -n "${context}" ]]; then
  kubectl_args+=(--context "${context}")
fi

talos_args=()
if [[ -n "${talosconfig}" ]]; then
  talos_args+=(--talosconfig "${talosconfig}")
fi
if [[ -n "${talos_endpoints}" ]]; then
  talos_args+=(--endpoints "${talos_endpoints}")
fi
if [[ -n "${talos_nodes}" ]]; then
  talos_args+=(--nodes "${talos_nodes}")
fi

record() {
  local name="$1"
  local status="$2"
  local path="$3"
  printf '%s\t%s\t%s\n' "${name}" "${status}" "${path}" >>"${summary}"
}

run_capture() {
  local name="$1"
  local file="$2"
  shift 2

  {
    printf '$'
    printf ' %q' "$@"
    printf '\n\n'
  } >"${file}"

  set +e
  "$@" >>"${file}" 2>&1
  local status=$?
  set -e

  record "${name}" "${status}" "${file}"
  if [[ "${status}" -ne 0 ]]; then
    failures=$((failures + 1))
  fi
}

run_api_vip_load() {
  local name="api-vip-load"
  local file="${out_dir}/evidence/api-vip-load.txt"
  local tmpdir
  local request
  local batch_size
  local batch_end
  local pid
  local status
  local request_failures
  local request_count
  local started
  local ended
  local elapsed

  tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/guardian-api-vip-load.XXXXXX")"
  {
    printf '$'
    printf ' %q' "${kubectl_bin}" "${kubectl_args[@]}" --server "${api_server}" get --raw=/readyz
    printf ' # repeated %s times with concurrency %s\n\n' "${api_load_requests}" "${api_load_concurrency}"
    printf 'api-vip-load server=%s requests=%s concurrency=%s path=/readyz\n' "${api_server}" "${api_load_requests}" "${api_load_concurrency}"
  } >"${file}"

  started="$(date +%s)"
  request=1
  while [[ "${request}" -le "${api_load_requests}" ]]; do
    batch_size=0
    batch_end=$((request + api_load_concurrency - 1))
    while [[ "${request}" -le "${api_load_requests}" && "${request}" -le "${batch_end}" ]]; do
      (
        set +e
        local output
        output="$("${kubectl_bin}" "${kubectl_args[@]}" --server "${api_server}" get --raw=/readyz 2>&1)"
        status=$?
        if [[ "${status}" -eq 0 ]] && grep -Eq '(^|[[:space:]])ok($|[[:space:]])' <<<"${output}"; then
          printf 'pass\t%s\n' "${request}" >"${tmpdir}/${request}.tsv"
        else
          printf 'fail\t%s\t%s\n%s\n' "${request}" "${status}" "${output}" >"${tmpdir}/${request}.tsv"
        fi
      ) &
      request=$((request + 1))
      batch_size=$((batch_size + 1))
    done
    for pid in $(jobs -p); do
      wait "${pid}" || true
    done
    if [[ "${batch_size}" -eq 0 ]]; then
      break
    fi
  done
  ended="$(date +%s)"
  elapsed=$((ended - started))

  request_count="$(find "${tmpdir}" -maxdepth 1 -type f -name '*.tsv' | wc -l | tr -d '[:space:]')"
  request_failures="$(awk -F '\t' '$1 == "fail" {count++} END {print count + 0}' "${tmpdir}"/*.tsv 2>/dev/null || true)"
  request_failures="${request_failures:-0}"
  {
    printf 'api-vip-load total=%s failures=%s concurrency=%s seconds=%s server=%s path=/readyz\n' \
      "${request_count}" "${request_failures}" "${api_load_concurrency}" "${elapsed}" "${api_server}"
    if [[ "${request_failures}" -ne 0 || "${request_count}" -ne "${api_load_requests}" ]]; then
      printf '\nfailed requests:\n'
      awk -F '\t' '$1 == "fail" {print FILENAME ": request=" $2 " status=" $3}' "${tmpdir}"/*.tsv 2>/dev/null || true
    fi
  } >>"${file}"

  rm -rf "${tmpdir}"

  if [[ "${request_failures}" -eq 0 && "${request_count}" -eq "${api_load_requests}" ]]; then
    record "${name}" "0" "${file}"
  else
    record "${name}" "1" "${file}"
    failures=$((failures + 1))
  fi
}

write_manifest() {
  cat >"${out_dir}/MANIFEST.md" <<EOF
# Management Evidence Capture

- Phase: ${phase}
- Captured at: ${timestamp}
- Output directory: ${out_dir}
- Kubernetes context: ${context:-default}
- Talos endpoints: ${talos_endpoints}
- Talos nodes: ${talos_nodes}
- API server: ${api_server}
- API load requests: ${api_load_requests}
- API load concurrency: ${api_load_concurrency}

Command statuses are recorded in \`summary.tsv\`. The capture avoids Kubernetes
Secret values: it records secret names only.
EOF
}

write_manifest

run_capture "kubectl-version" "${out_dir}/kubectl/version.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" version
run_capture "kubectl-nodes" "${out_dir}/kubectl/nodes-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get nodes -o wide
run_capture "kubectl-storageclasses" "${out_dir}/kubectl/storageclasses-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get storageclass -o wide
run_capture "kubectl-pv" "${out_dir}/kubectl/pv-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get pv -o wide
run_capture "kubectl-pvc" "${out_dir}/kubectl/pvc-all-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get pvc -A -o wide
run_capture "kubectl-secret-contracts" "${out_dir}/kubectl/secret-contracts.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get secret guardian-r2-db-backups guardian-openbao-evidence-token -n tenant-root -o name --ignore-not-found
run_capture "kubectl-packages" "${out_dir}/kubectl/packages-all-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get packages -A -o wide
run_capture "kubectl-tenants" "${out_dir}/kubectl/tenants-all-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get tenants -A -o wide
run_capture "kubectl-backupclasses" "${out_dir}/kubectl/backupclasses-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get backupclasses -o wide
run_capture "kubectl-plans" "${out_dir}/kubectl/plans-all-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get plans -A -o wide
run_capture "kubectl-backupjobs" "${out_dir}/kubectl/backupjobs-all-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get backupjobs -A -o wide
run_capture "kubectl-backups" "${out_dir}/kubectl/backups-all-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get backups -A -o wide
run_capture "kubectl-apps" "${out_dir}/kubectl/root-apps.yaml" \
  "${kubectl_bin}" "${kubectl_args[@]}" get harbor/oci clickhouse/ledger postgres/guardian openbao/guardian -n tenant-root -o yaml --ignore-not-found
run_capture "kubectl-company-dev" "${out_dir}/kubectl/company-site-dev-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get deploy/company-site svc/company-site ing/company-site -n tenant-dev -o wide --ignore-not-found
run_capture "kubectl-company-gamma" "${out_dir}/kubectl/company-site-gamma-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get deploy/company-site svc/company-site ing/company-site -n tenant-gamma -o wide --ignore-not-found
run_capture "kubectl-company-prod" "${out_dir}/kubectl/company-site-prod-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get deploy/company-site svc/company-site ing/company-site -n tenant-root -o wide --ignore-not-found
run_capture "kubectl-certificates" "${out_dir}/kubectl/certificates-all-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get certificate -A -o wide
run_capture "kubectl-ingress" "${out_dir}/kubectl/ingress-all-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get ingress -A -o wide
run_capture "kubectl-pods" "${out_dir}/kubectl/pods-all-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get pods -A -o wide

run_api_vip_load

run_capture "evidence-jobs" "${out_dir}/evidence/jobs-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get job/evidence-postgres-load job/evidence-clickhouse-load job/evidence-harbor-oci-read job/evidence-openbao-load job/evidence-http-load job/evidence-storage-smoke -n tenant-root -o wide --ignore-not-found
run_capture "evidence-backupjobs" "${out_dir}/evidence/backupjobs.yaml" \
  "${kubectl_bin}" "${kubectl_args[@]}" get backupjob/evidence-postgres-adhoc backupjob/evidence-clickhouse-adhoc -n tenant-root -o yaml --ignore-not-found
run_capture "evidence-restorejobs" "${out_dir}/evidence/restorejobs.yaml" \
  "${kubectl_bin}" "${kubectl_args[@]}" get restorejob/evidence-postgres-to-copy restorejob/evidence-clickhouse-to-copy -n tenant-root -o yaml --ignore-not-found
run_capture "evidence-restore-targets" "${out_dir}/evidence/restore-targets-wide.txt" \
  "${kubectl_bin}" "${kubectl_args[@]}" get postgres/guardian-restore-check clickhouse/ledger-restore-check -n tenant-root -o wide --ignore-not-found

for job in evidence-postgres-load evidence-clickhouse-load evidence-harbor-oci-read evidence-openbao-load evidence-http-load evidence-storage-smoke; do
  run_capture "logs-${job}" "${out_dir}/evidence/logs-${job}.txt" \
    "${kubectl_bin}" "${kubectl_args[@]}" logs "job/${job}" -n tenant-root --ignore-errors
done

if [[ -n "${talosconfig}" ]]; then
  run_capture "talos-health" "${out_dir}/talos/health.txt" \
    "${talosctl_bin}" "${talos_args[@]}" health
  run_capture "talos-etcd-members" "${out_dir}/talos/etcd-members.txt" \
    "${talosctl_bin}" "${talos_args[@]}" etcd members
else
  record "talos-health" "skipped" "${out_dir}/talos/health.txt"
  record "talos-etcd-members" "skipped" "${out_dir}/talos/etcd-members.txt"
fi

echo "wrote management evidence capture to ${out_dir}"
echo "command summary: ${summary}"

if [[ "${failures}" -ne 0 && "${allow_failures}" != "true" ]]; then
  echo "capture completed with ${failures} command failure(s)" >&2
  exit 1
fi

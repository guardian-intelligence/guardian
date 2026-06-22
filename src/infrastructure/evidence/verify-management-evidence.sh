#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: verify-management-evidence --run-dir DIR [OPTIONS]

Verifies a captured management evidence directory written by
capture-management-evidence. The verifier reads only checked-in capture files
and writes VERIFY.md plus verification.tsv into the run directory.

Options:
  --run-dir DIR            captured live-run directory to verify
  --phase NAME             phase label; defaults to MANIFEST.md
  --mode MODE              common, evidence, or outage; defaults from phase
  --min-ready-nodes N      minimum Ready nodes required; defaults from phase
  --require-talos          require Talos health and etcd member captures
  --node NAME              optional outage node name to check in nodes output
  -h, --help               show this help
EOF
}

run_dir=""
phase=""
mode=""
min_ready_nodes=""
require_talos=false
node=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --run-dir)
      run_dir="${2:?--run-dir requires a value}"
      shift 2
      ;;
    --phase)
      phase="${2:?--phase requires a value}"
      shift 2
      ;;
    --mode)
      mode="${2:?--mode requires a value}"
      shift 2
      ;;
    --min-ready-nodes)
      min_ready_nodes="${2:?--min-ready-nodes requires a value}"
      shift 2
      ;;
    --require-talos)
      require_talos=true
      shift
      ;;
    --node)
      node="${2:?--node requires a value}"
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

if [[ -z "${run_dir}" ]]; then
  echo "ERROR: pass --run-dir with a captured evidence directory" >&2
  usage >&2
  exit 2
fi
if [[ ! -d "${run_dir}" ]]; then
  echo "ERROR: run directory does not exist: ${run_dir}" >&2
  exit 1
fi

manifest="${run_dir}/MANIFEST.md"
summary="${run_dir}/summary.tsv"
verification="${run_dir}/verification.tsv"
report="${run_dir}/VERIFY.md"
checks=0
failures=0

if [[ -z "${phase}" && -f "${manifest}" ]]; then
  phase="$(awk -F ': ' '$1 == "- Phase" {print $2; exit}' "${manifest}")"
fi
phase="${phase:-unknown}"

if [[ -z "${mode}" ]]; then
  case "${phase}" in
    evidence)
      mode="evidence"
      ;;
    outage-*)
      mode="outage"
      ;;
    *)
      mode="common"
      ;;
  esac
fi

if [[ -z "${min_ready_nodes}" ]]; then
  if [[ "${phase}" == "outage-down" ]]; then
    min_ready_nodes=2
  else
    min_ready_nodes=3
  fi
fi

: >"${verification}"

record_check() {
  local status="$1"
  local name="$2"
  local detail="$3"

  checks=$((checks + 1))
  printf '%s\t%s\t%s\n' "${status}" "${name}" "${detail}" >>"${verification}"
  if [[ "${status}" == "fail" ]]; then
    failures=$((failures + 1))
  fi
}

pass() {
  record_check "pass" "$1" "$2"
}

fail() {
  record_check "fail" "$1" "$2"
}

skip() {
  record_check "skip" "$1" "$2"
}

require_file() {
  local rel="$1"
  local path="${run_dir}/${rel}"

  if [[ -s "${path}" ]]; then
    pass "file:${rel}" "present"
  else
    fail "file:${rel}" "missing or empty"
  fi
}

grep_file() {
  local rel="$1"
  local pattern="$2"
  local name="$3"
  local path="${run_dir}/${rel}"

  if [[ ! -s "${path}" ]]; then
    fail "${name}" "missing ${rel}"
    return
  fi
  if grep -Eq "${pattern}" "${path}"; then
    pass "${name}" "matched ${rel}"
  else
    fail "${name}" "pattern not found in ${rel}: ${pattern}"
  fi
}

count_file() {
  local rel="$1"
  local pattern="$2"
  local minimum="$3"
  local name="$4"
  local path="${run_dir}/${rel}"
  local count

  if [[ ! -s "${path}" ]]; then
    fail "${name}" "missing ${rel}"
    return
  fi
  count="$(grep -Ec "${pattern}" "${path}" || true)"
  if [[ "${count}" -ge "${minimum}" ]]; then
    pass "${name}" "matched ${count}; required ${minimum}"
  else
    fail "${name}" "matched ${count}; required ${minimum}"
  fi
}

summary_status() {
  local name="$1"
  local allow_skipped="${2:-false}"
  local allow_failed="${3:-false}"
  local line
  local status
  local path

  if [[ ! -s "${summary}" ]]; then
    fail "summary:${name}" "missing summary.tsv"
    return
  fi

  line="$(awk -F '\t' -v name="${name}" '$1 == name {print; exit}' "${summary}")"
  if [[ -z "${line}" ]]; then
    fail "summary:${name}" "missing command status"
    return
  fi

  status="$(printf '%s\n' "${line}" | awk -F '\t' '{print $2}')"
  path="$(printf '%s\n' "${line}" | awk -F '\t' '{print $3}')"
  case "${status}" in
    0)
      pass "summary:${name}" "${path}"
      ;;
    skipped)
      if [[ "${allow_skipped}" == "true" ]]; then
        skip "summary:${name}" "${path}"
      else
        fail "summary:${name}" "skipped"
      fi
      ;;
    *)
      if [[ "${allow_failed}" == "true" ]]; then
        skip "summary:${name}" "allowed degraded status ${status}; ${path}"
      else
        fail "summary:${name}" "exit status ${status}; ${path}"
      fi
      ;;
  esac
}

verify_ready_nodes() {
  local rel="kubectl/nodes-wide.txt"
  local path="${run_dir}/${rel}"
  local count

  if [[ ! -s "${path}" ]]; then
    fail "nodes:ready" "missing ${rel}"
    return
  fi

  count="$(awk '
    $1 == "$" {next}
    $1 == "NAME" {next}
    $2 ~ /Ready/ {ready++}
    END {print ready + 0}
  ' "${path}")"

  if [[ "${count}" -ge "${min_ready_nodes}" ]]; then
    pass "nodes:ready" "Ready nodes ${count}; required ${min_ready_nodes}"
  else
    fail "nodes:ready" "Ready nodes ${count}; required ${min_ready_nodes}"
  fi
}

verify_outage_node() {
  if [[ -z "${node}" ]]; then
    skip "outage:node" "no --node supplied"
    return
  fi

  grep_file "kubectl/nodes-wide.txt" "^${node}[[:space:]]" "outage:node-present"
  if [[ "${phase}" == "outage-drained" ]]; then
    grep_file "kubectl/nodes-wide.txt" "^${node}[[:space:]]+Ready,SchedulingDisabled" "outage:node-drained"
  fi
}

verify_common() {
  local allow_degraded_talos=false
  if [[ "${phase}" == "outage-down" && "${require_talos}" != "true" ]]; then
    allow_degraded_talos=true
  fi

  require_file "MANIFEST.md"
  require_file "summary.tsv"

  for name in \
    kubectl-version \
    kubectl-nodes \
    kubectl-storageclasses \
    kubectl-pv \
    kubectl-pvc \
    kubectl-secret-contracts \
    kubectl-packages \
    kubectl-tenants \
    kubectl-backupclasses \
    kubectl-plans \
    kubectl-backupjobs \
    kubectl-backups \
    kubectl-apps \
    kubectl-company-dev \
    kubectl-company-gamma \
    kubectl-company-prod \
    kubectl-certificates \
    kubectl-ingress \
    kubectl-pods; do
    summary_status "${name}"
  done

  if [[ "${require_talos}" == "true" ]]; then
    summary_status "talos-health"
    summary_status "talos-etcd-members"
  else
    summary_status "talos-health" "true" "${allow_degraded_talos}"
    summary_status "talos-etcd-members" "true" "${allow_degraded_talos}"
  fi

  verify_ready_nodes
  grep_file "kubectl/storageclasses-wide.txt" "replicated" "storageclass:replicated"
  grep_file "kubectl/storageclasses-wide.txt" "replicated-retain" "storageclass:replicated-retain"
  grep_file "kubectl/secret-contracts.txt" "secret/guardian-r2-db-backups" "secret:r2-backups"
  grep_file "kubectl/secret-contracts.txt" "secret/guardian-openbao-evidence-token" "secret:openbao-evidence-token"

  grep_file "kubectl/root-apps.yaml" "kind: Harbor" "app:harbor-kind"
  grep_file "kubectl/root-apps.yaml" "name: oci" "app:harbor-name"
  grep_file "kubectl/root-apps.yaml" "kind: ClickHouse" "app:clickhouse-kind"
  grep_file "kubectl/root-apps.yaml" "name: ledger" "app:clickhouse-name"
  grep_file "kubectl/root-apps.yaml" "kind: Postgres" "app:postgres-kind"
  grep_file "kubectl/root-apps.yaml" "kind: OpenBAO" "app:openbao-kind"
  count_file "kubectl/root-apps.yaml" "name: guardian" 2 "app:guardian-names"

  for env in dev gamma prod; do
    grep_file "kubectl/company-site-${env}-wide.txt" "deployment.apps/company-site" "company-site:${env}:deployment"
    grep_file "kubectl/company-site-${env}-wide.txt" "service/company-site" "company-site:${env}:service"
    grep_file "kubectl/company-site-${env}-wide.txt" "ingress.networking.k8s.io/company-site" "company-site:${env}:ingress"
  done

  for host in \
    guardianintelligence.org \
    dev.guardianintelligence.org \
    gamma.guardianintelligence.org \
    oci.guardianintelligence.org \
    dashboard.guardianintelligence.org; do
    grep_file "kubectl/ingress-all-wide.txt" "${host}" "ingress:${host}"
  done
}

verify_evidence() {
  for name in \
    evidence-jobs \
    evidence-backupjobs \
    evidence-restorejobs \
    evidence-restore-targets \
    logs-evidence-postgres-load \
    logs-evidence-clickhouse-load \
    logs-evidence-harbor-oci-read \
    logs-evidence-openbao-load \
    logs-evidence-http-load \
    logs-evidence-storage-smoke; do
    summary_status "${name}"
  done

  for job in \
    evidence-postgres-load \
    evidence-clickhouse-load \
    evidence-harbor-oci-read \
    evidence-openbao-load \
    evidence-http-load \
    evidence-storage-smoke; do
    grep_file "evidence/jobs-wide.txt" "${job}[[:space:]]+1/1" "evidence-job:${job}"
  done

  grep_file "evidence/logs-evidence-postgres-load.txt" "postgres-load .*expected=1000 actual=1000" "load:postgres"
  grep_file "evidence/logs-evidence-clickhouse-load.txt" "clickhouse-load .*expected=1000 actual=1000" "load:clickhouse"
  grep_file "evidence/logs-evidence-harbor-oci-read.txt" "harbor-oci-read total=25 failures=0" "load:harbor"
  grep_file "evidence/logs-evidence-openbao-load.txt" "openbao-load total=25 failures=0" "load:openbao"
  grep_file "evidence/logs-evidence-http-load.txt" "http-load total=1700 failures=0" "load:http"
  grep_file "evidence/logs-evidence-storage-smoke.txt" "storage-smoke files=64" "load:storage"

  grep_file "evidence/backupjobs.yaml" "name: evidence-postgres-adhoc" "dr:postgres-backupjob"
  grep_file "evidence/backupjobs.yaml" "name: evidence-clickhouse-adhoc" "dr:clickhouse-backupjob"
  count_file "evidence/backupjobs.yaml" "Succeeded" 2 "dr:backupjobs-succeeded"
  grep_file "evidence/restorejobs.yaml" "name: evidence-postgres-to-copy" "dr:postgres-restorejob"
  grep_file "evidence/restorejobs.yaml" "name: evidence-clickhouse-to-copy" "dr:clickhouse-restorejob"
  count_file "evidence/restorejobs.yaml" "Succeeded" 2 "dr:restorejobs-succeeded"
  grep_file "evidence/restore-targets-wide.txt" "guardian-restore-check" "dr:postgres-restore-target"
  grep_file "evidence/restore-targets-wide.txt" "ledger-restore-check" "dr:clickhouse-restore-target"
}

write_report() {
  local result="PASS"
  if [[ "${failures}" -ne 0 ]]; then
    result="FAIL"
  fi

  cat >"${report}" <<EOF
# Management Evidence Verification

- Run directory: ${run_dir}
- Phase: ${phase}
- Mode: ${mode}
- Minimum Ready nodes: ${min_ready_nodes}
- Talos required: ${require_talos}
- Result: ${result}
- Checks: ${checks}
- Failures: ${failures}

Detailed check results are in \`verification.tsv\`.
EOF
}

case "${mode}" in
  common|evidence|outage)
    ;;
  *)
    echo "ERROR: unsupported --mode ${mode}; expected common, evidence, or outage" >&2
    exit 2
    ;;
esac

verify_common
case "${mode}" in
  evidence)
    verify_evidence
    ;;
  outage)
    verify_outage_node
    ;;
esac

write_report

echo "wrote ${verification}"
echo "wrote ${report}"

if [[ "${failures}" -ne 0 ]]; then
  echo "verification failed with ${failures} failed check(s)" >&2
  exit 1
fi

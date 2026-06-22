#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: verify-management-evidence-suite --evidence-dir DIR --hardware-outage-dir DIR [OPTIONS]

Verifies a complete checked-in management evidence package. The verifier reads
only captured evidence directories and writes SUITE.md plus
suite-verification.tsv into the suite output directory.

Options:
  --evidence-dir DIR          verified load/DR evidence capture directory
  --hardware-outage-dir DIR   all-node hardware outage parent directory
  --inventory PATH            inventory JSON; defaults to guardian-mgmt inventory
  --nodes CSV                 optional expected node subset; defaults to inventory
  --out-dir DIR               output directory; defaults under live-runs/
  -h, --help                  show this help

Inputs:
  MANAGEMENT_INVENTORY_BIN    repo-pinned inventory reader, required unless --nodes is set
EOF
}

evidence_dir=""
hardware_outage_dir=""
inventory="src/infrastructure/inventory/guardian-mgmt.json"
nodes_csv=""
out_dir=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --evidence-dir)
      evidence_dir="${2:?--evidence-dir requires a value}"
      shift 2
      ;;
    --hardware-outage-dir)
      hardware_outage_dir="${2:?--hardware-outage-dir requires a value}"
      shift 2
      ;;
    --inventory)
      inventory="${2:?--inventory requires a value}"
      shift 2
      ;;
    --nodes)
      nodes_csv="${2:?--nodes requires a value}"
      shift 2
      ;;
    --out-dir)
      out_dir="${2:?--out-dir requires a value}"
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

if [[ -z "${evidence_dir}" ]]; then
  echo "ERROR: pass --evidence-dir with a verified load/DR evidence capture" >&2
  exit 2
fi
if [[ -z "${hardware_outage_dir}" ]]; then
  echo "ERROR: pass --hardware-outage-dir with an all-node hardware outage capture" >&2
  exit 2
fi

nodes=()
if [[ -n "${nodes_csv}" ]]; then
  IFS=',' read -r -a nodes <<<"${nodes_csv}"
else
  management_inventory_bin="${MANAGEMENT_INVENTORY_BIN:-}"
  if [[ -z "${management_inventory_bin}" || ! -x "${management_inventory_bin}" ]]; then
    echo "ERROR: MANAGEMENT_INVENTORY_BIN must be an executable unless --nodes is set" >&2
    exit 1
  fi
  mapfile -t nodes < <("${management_inventory_bin}" --inventory "${inventory}" nodes)
fi

filtered_nodes=()
for node in "${nodes[@]}"; do
  node="${node#"${node%%[![:space:]]*}"}"
  node="${node%"${node##*[![:space:]]}"}"
  if [[ -n "${node}" ]]; then
    filtered_nodes+=("${node}")
  fi
done
nodes=("${filtered_nodes[@]}")

if [[ "${#nodes[@]}" -eq 0 ]]; then
  echo "ERROR: no management nodes selected" >&2
  exit 1
fi

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
if [[ -z "${out_dir}" ]]; then
  out_dir="docs/reports/infrastructure/live-runs/${timestamp}-management-suite"
fi
mkdir -p "${out_dir}"

verification="${out_dir}/suite-verification.tsv"
report="${out_dir}/SUITE.md"
checks=0
failures=0

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

require_dir() {
  local path="$1"
  local name="$2"

  if [[ -d "${path}" ]]; then
    pass "${name}" "${path}"
  else
    fail "${name}" "missing directory: ${path}"
  fi
}

require_file() {
  local path="$1"
  local name="$2"

  if [[ -s "${path}" ]]; then
    pass "${name}" "${path}"
  else
    fail "${name}" "missing or empty file: ${path}"
  fi
}

grep_file() {
  local path="$1"
  local pattern="$2"
  local name="$3"

  if [[ ! -s "${path}" ]]; then
    fail "${name}" "missing ${path}"
    return
  fi
  if grep -Eq "${pattern}" "${path}"; then
    pass "${name}" "matched ${path}"
  else
    fail "${name}" "pattern not found in ${path}: ${pattern}"
  fi
}

grep_file_fixed_line() {
  local path="$1"
  local line="$2"
  local name="$3"

  if [[ ! -s "${path}" ]]; then
    fail "${name}" "missing ${path}"
    return
  fi
  if grep -Fqx "${line}" "${path}"; then
    pass "${name}" "matched ${path}"
  else
    fail "${name}" "line not found in ${path}: ${line}"
  fi
}

verify_no_failures_tsv() {
  local path="$1"
  local name="$2"
  local count

  if [[ ! -s "${path}" ]]; then
    fail "${name}" "missing ${path}"
    return
  fi
  count="$(awk -F '\t' '$1 == "fail" {count++} END {print count + 0}' "${path}")"
  if [[ "${count}" -eq 0 ]]; then
    pass "${name}" "no failed checks"
  else
    fail "${name}" "${count} failed check(s)"
  fi
}

require_passed_verification() {
  local run_dir="$1"
  local label="$2"

  require_dir "${run_dir}" "${label}:dir"
  require_file "${run_dir}/MANIFEST.md" "${label}:manifest"
  require_file "${run_dir}/VERIFY.md" "${label}:verify-report"
  require_file "${run_dir}/verification.tsv" "${label}:verification-tsv"
  grep_file "${run_dir}/VERIFY.md" "^- Result: PASS$" "${label}:result-pass"
  verify_no_failures_tsv "${run_dir}/verification.tsv" "${label}:no-failed-checks"
}

require_verification_check() {
  local run_dir="$1"
  local check_name="$2"
  local label="$3"
  local path="${run_dir}/verification.tsv"

  if [[ ! -s "${path}" ]]; then
    fail "${label}" "missing ${path}"
    return
  fi
  if awk -F '\t' -v check_name="${check_name}" '$1 == "pass" && $2 == check_name {found = 1} END {exit found ? 0 : 1}' "${path}"; then
    pass "${label}" "${check_name}"
  else
    fail "${label}" "passing check not found: ${check_name}"
  fi
}

verify_evidence_package() {
  require_passed_verification "${evidence_dir}" "evidence"
  grep_file "${evidence_dir}/VERIFY.md" "^- Phase: evidence$" "evidence:phase"
  grep_file "${evidence_dir}/VERIFY.md" "^- Mode: evidence$" "evidence:mode"
  grep_file "${evidence_dir}/VERIFY.md" "^- Talos required: true$" "evidence:talos-required"

  for check_name in \
    load:postgres \
    load:clickhouse \
    load:harbor \
    load:openbao \
    load:http \
    load:http:company-prod-root \
    load:http:company-prod-letters \
    load:http:company-prod-news \
    load:http:company-prod-healthz \
    load:http:company-prod-metrics \
    load:http:company-dev-root \
    load:http:company-dev-letters \
    load:http:company-dev-news \
    load:http:company-dev-healthz \
    load:http:company-dev-metrics \
    load:http:company-gamma-root \
    load:http:company-gamma-letters \
    load:http:company-gamma-news \
    load:http:company-gamma-healthz \
    load:http:company-gamma-metrics \
    load:http:harbor-health \
    load:http:dashboard-root \
    load:storage \
    dr:postgres-backupjob \
    dr:clickhouse-backupjob \
    dr:backupjobs-succeeded \
    dr:postgres-restorejob \
    dr:clickhouse-restorejob \
    dr:restorejobs-succeeded \
    dr:postgres-restore-target \
    dr:clickhouse-restore-target \
    api:vip-load \
    app:harbor-kind \
    app:clickhouse-kind \
    app:postgres-kind \
    app:openbao-kind \
    ingress:guardianintelligence.org \
    ingress:dev.guardianintelligence.org \
    ingress:gamma.guardianintelligence.org \
    ingress:oci.guardianintelligence.org \
    ingress:dashboard.guardianintelligence.org; do
    require_verification_check "${evidence_dir}" "${check_name}" "evidence-check:${check_name}"
  done
}

verify_latitude_file() {
  local node="$1"
  local path="$2"
  local label="$3"
  local expected_action="$4"
  local expected_status="$5"

  require_file "${path}" "${label}:file"
  grep_file "${path}" "\"node\":\"${node}\"" "${label}:node"
  grep_file "${path}" "\"action\":\"${expected_action}\"" "${label}:action-${expected_action}"
  if [[ "${expected_action}" != "status" ]]; then
    grep_file "${path}" "\"http_status\":2[0-9][0-9]" "${label}:http-2xx"
  fi
  grep_file "${path}" "\"action\":\"status\"" "${label}:status-action"
  grep_file "${path}" "\"status\":\"${expected_status}\"" "${label}:status-${expected_status}"
}

verify_outage_phase() {
  local node="$1"
  local node_dir="$2"
  local phase="$3"
  local min_ready_nodes="$4"
  local talos_required="$5"
  local phase_dir="${node_dir}/${phase}"
  local label="hardware:${node}:${phase}"

  require_passed_verification "${phase_dir}" "${label}"
  grep_file "${phase_dir}/VERIFY.md" "^- Phase: ${phase}$" "${label}:phase"
  grep_file "${phase_dir}/VERIFY.md" "^- Mode: outage$" "${label}:mode"
  grep_file "${phase_dir}/VERIFY.md" "^- Minimum Ready nodes: ${min_ready_nodes}$" "${label}:min-ready"
  grep_file "${phase_dir}/VERIFY.md" "^- Talos required: ${talos_required}$" "${label}:talos-required"
  require_verification_check "${phase_dir}" "outage:node-present" "${label}:node-present"
  require_verification_check "${phase_dir}" "nodes:ready" "${label}:nodes-ready"
}

verify_hardware_outage_package() {
  require_dir "${hardware_outage_dir}" "hardware:dir"
  require_file "${hardware_outage_dir}/MANIFEST.md" "hardware:manifest"

  for node in "${nodes[@]}"; do
    grep_file_fixed_line "${hardware_outage_dir}/MANIFEST.md" "  - ${node}" "hardware:${node}:listed"

    node_dir="${hardware_outage_dir}/${node}"
    require_dir "${node_dir}" "hardware:${node}:dir"
    require_file "${node_dir}/MANIFEST.md" "hardware:${node}:manifest"
    grep_file "${node_dir}/MANIFEST.md" "^- Node: ${node}$" "hardware:${node}:manifest-node"

    verify_latitude_file "${node}" "${node_dir}/latitude-before.jsonl" "hardware:${node}:latitude-before" "status" "on"
    verify_latitude_file "${node}" "${node_dir}/latitude-down.jsonl" "hardware:${node}:latitude-down" "power_off" "off"
    verify_latitude_file "${node}" "${node_dir}/latitude-after.jsonl" "hardware:${node}:latitude-after" "power_on" "on"

    verify_outage_phase "${node}" "${node_dir}" "outage-before" 3 true
    verify_outage_phase "${node}" "${node_dir}" "outage-down" 2 false
    verify_outage_phase "${node}" "${node_dir}" "outage-after" 3 true
  done
}

write_report() {
  local result="PASS"
  if [[ "${failures}" -ne 0 ]]; then
    result="FAIL"
  fi

  {
    echo "# Management Evidence Suite Verification"
    echo
    echo "- Evidence directory: ${evidence_dir}"
    echo "- Hardware outage directory: ${hardware_outage_dir}"
    echo "- Inventory: ${inventory}"
    echo "- Output directory: ${out_dir}"
    echo "- Verified at: ${timestamp}"
    echo "- Result: ${result}"
    echo "- Checks: ${checks}"
    echo "- Failures: ${failures}"
    echo "- Nodes:"
    for node in "${nodes[@]}"; do
      echo "  - ${node}"
    done
    echo
    echo "Detailed check results are in \`suite-verification.tsv\`."
  } >"${report}"
}

verify_evidence_package
verify_hardware_outage_package
write_report

echo "wrote ${verification}"
echo "wrote ${report}"

if [[ "${failures}" -ne 0 ]]; then
  echo "suite verification failed with ${failures} failed check(s)" >&2
  exit 1
fi

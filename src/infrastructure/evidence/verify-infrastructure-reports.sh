#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: verify-infrastructure-reports --live-run-dir DIR [OPTIONS]

Verifies the checked-in infrastructure component reports after live evidence has
been collected. This is a final PR gate: it reads only report files and the
checked-in live-run package, then writes a report-verification.tsv file.

Options:
  --live-run-dir DIR   final docs/reports/infrastructure/live-runs/<run> dir
  --reports-dir DIR    reports directory; default docs/reports/infrastructure
  --out DIR            verification TSV path; default <live-run-dir>/report-verification.tsv
  -h, --help           show this help
EOF
}

reports_dir="docs/reports/infrastructure"
live_run_dir=""
out=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --live-run-dir)
      live_run_dir="${2:?--live-run-dir requires a value}"
      shift 2
      ;;
    --reports-dir)
      reports_dir="${2:?--reports-dir requires a value}"
      shift 2
      ;;
    --out)
      out="${2:?--out requires a value}"
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

if [[ -z "${live_run_dir}" ]]; then
  echo "ERROR: pass --live-run-dir with the final management-evidence-run output" >&2
  exit 2
fi
if [[ -z "${out}" ]]; then
  out="${live_run_dir}/report-verification.tsv"
fi

component_reports=(
  2026-06-22-talos-kubernetes-api-vip.md
  2026-06-22-linstor-drbd-storage.md
  2026-06-22-openbao.md
  2026-06-22-cnpg-postgres.md
  2026-06-22-harbor.md
  2026-06-22-clickhouse.md
  2026-06-22-cozystack-dashboard.md
  2026-06-22-public-ingress-dns.md
  2026-06-22-dev-tenant.md
  2026-06-22-gamma-tenant.md
  2026-06-22-prod-root-tenant.md
  2026-06-22-company-site.md
)

required_sections=(
  "## Scope"
  "## Preflight"
  "## Load Test"
  "## Disaster Recovery Drill"
  "## Single-Node Outage Exercise"
  "## Residual Risk"
)

checks=0
failures=0
: >"${out}"

record_check() {
  local status="$1"
  local name="$2"
  local detail="$3"

  checks=$((checks + 1))
  printf '%s\t%s\t%s\n' "${status}" "${name}" "${detail}" >>"${out}"
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

require_file() {
  local path="$1"
  local name="$2"

  if [[ -s "${path}" ]]; then
    pass "${name}" "${path}"
  else
    fail "${name}" "missing or empty: ${path}"
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

reject_file() {
  local path="$1"
  local pattern="$2"
  local name="$3"

  if [[ ! -s "${path}" ]]; then
    fail "${name}" "missing ${path}"
    return
  fi
  if grep -Eiq "${pattern}" "${path}"; then
    fail "${name}" "rejected pattern found in ${path}: ${pattern}"
  else
    pass "${name}" "not present in ${path}"
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

run_basename="$(basename "${live_run_dir}")"
suite_dir="${live_run_dir}/management-suite"
matrix="${reports_dir}/evidence-matrix.md"

require_file "${live_run_dir}/MANIFEST.md" "live-run:manifest"
require_file "${suite_dir}/SUITE.md" "suite:report"
require_file "${suite_dir}/suite-verification.tsv" "suite:verification-tsv"
grep_file "${suite_dir}/SUITE.md" "^- Result: PASS$" "suite:result-pass"
verify_no_failures_tsv "${suite_dir}/suite-verification.tsv" "suite:no-failed-checks"

require_file "${matrix}" "matrix:file"
reject_file "${matrix}" "pending|not applied|not passed|live execution pending" "matrix:no-pending"
grep_file "${matrix}" "Status[[:space:]]*\\|" "matrix:status-column"

for report in "${component_reports[@]}"; do
  path="${reports_dir}/${report}"
  stem="${report%.md}"
  require_file "${path}" "report:${stem}:file"

  for section in "${required_sections[@]}"; do
    grep_file "${path}" "^${section}$" "report:${stem}:section:${section#\#\# }"
  done

  reject_file "${path}" "pending live execution|Result:[[:space:]]*pending|Result:[[:space:]]*$|Status:[[:space:]]*pending" "report:${stem}:no-pending"
  grep_file "${path}" "live-runs/${run_basename}" "report:${stem}:live-run-reference"
  grep_file "${path}" "Result:[[:space:]]*(PASS|pass|Passed|passed)" "report:${stem}:has-pass-result"
  grep_file "${path}" "Evidence:" "report:${stem}:has-evidence"
done

echo "wrote ${out}"
echo "report verification checks=${checks} failures=${failures}"

if [[ "${failures}" -ne 0 ]]; then
  echo "report verification failed with ${failures} failed check(s)" >&2
  exit 1
fi

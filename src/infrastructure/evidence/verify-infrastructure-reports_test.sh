#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 2 ]]; then
  echo "usage: verify-infrastructure-reports_test.sh VERIFY_SCRIPT OUT_FILE" >&2
  exit 2
fi

script="$1"
out_file="$2"
tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/infrastructure-reports-fixture.XXXXXX")"
reports_dir="${tmpdir}/reports"
live_run_dir="${reports_dir}/live-runs/20260622T000000Z-management-evidence"
suite_dir="${live_run_dir}/management-suite"
trap 'rm -rf "${tmpdir}"' EXIT

mkdir -p "${reports_dir}" "${suite_dir}"

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

cat >"${live_run_dir}/MANIFEST.md" <<'EOF'
# Management Evidence Run
EOF

cat >"${suite_dir}/SUITE.md" <<'EOF'
# Management Evidence Suite Verification

- Result: PASS
EOF
printf '%s\n' 'pass	suite	fixture' >"${suite_dir}/suite-verification.tsv"

{
  printf '%s\n' '# Management Infrastructure Evidence Matrix'
  printf '\n'
  printf '%s\n' '| Component | Desired state source | Load test report | DR drill report | Single-node outage report | Status |'
  printf '%s\n' '| - | - | - | - | - | - |'
  for report in "${component_reports[@]}"; do
    printf '| %s | source | `%s` | same report | same report | PASS |\n' "${report%.md}" "${report}"
  done
} >"${reports_dir}/evidence-matrix.md"

write_report() {
  local path="$1"
  local component="$2"

  cat >"${path}" <<EOF
# ${component} Operational Report

## Scope

- Component: ${component}
- Status: PASS

## Preflight

- Result: PASS
- Evidence: live-runs/20260622T000000Z-management-evidence/management-suite/SUITE.md

## Load Test

- Result: PASS
- Evidence: live-runs/20260622T000000Z-management-evidence/evidence/verification.tsv

## Disaster Recovery Drill

- Result: PASS
- Evidence: live-runs/20260622T000000Z-management-evidence/evidence/verification.tsv

## Single-Node Outage Exercise

- Result: PASS
- Evidence: live-runs/20260622T000000Z-management-evidence/hardware-outage-all/

## Residual Risk

- Remaining gaps: none for fixture.
EOF
}

for report in "${component_reports[@]}"; do
  write_report "${reports_dir}/${report}" "${report%.md}"
done

bash "${script}" \
  --reports-dir "${reports_dir}" \
  --live-run-dir "${live_run_dir}" \
  --out "${tmpdir}/report-verification.tsv"

test -s "${tmpdir}/report-verification.tsv"
awk -F '\t' '$1 == "fail" {failures++} END {exit (failures + 0) == 0 ? 0 : 1}' "${tmpdir}/report-verification.tsv"
printf 'ok\n' >"${out_file}"

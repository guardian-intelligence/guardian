#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 2 ]]; then
  echo "usage: verify-management-evidence-suite_test.sh SUITE_SCRIPT OUT_FILE" >&2
  exit 2
fi

script="$1"
out_file="$2"
tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/management-suite-fixture.XXXXXX")"
evidence_dir="${tmpdir}/evidence"
hardware_dir="${tmpdir}/hardware"
suite_dir="${tmpdir}/suite"

mkdir -p "${evidence_dir}" "${hardware_dir}" "${suite_dir}"
trap 'rm -rf "${tmpdir}"' EXIT

required_evidence_checks=(
  load:postgres
  load:clickhouse
  load:harbor
  load:openbao
  load:http
  load:http:company-prod-root
  load:http:company-prod-letters
  load:http:company-prod-news
  load:http:company-prod-healthz
  load:http:company-prod-metrics
  load:http:company-dev-root
  load:http:company-dev-letters
  load:http:company-dev-news
  load:http:company-dev-healthz
  load:http:company-dev-metrics
  load:http:company-gamma-root
  load:http:company-gamma-letters
  load:http:company-gamma-news
  load:http:company-gamma-healthz
  load:http:company-gamma-metrics
  load:http:harbor-health
  load:http:dashboard-root
  load:storage
  dr:postgres-backupjob
  dr:clickhouse-backupjob
  dr:backupjobs-succeeded
  dr:postgres-restorejob
  dr:clickhouse-restorejob
  dr:restorejobs-succeeded
  dr:postgres-restore-verify-job
  dr:clickhouse-restore-verify-job
  dr:postgres-restore-verify
  dr:clickhouse-restore-verify
  dr:postgres-restore-target
  dr:clickhouse-restore-target
  api:vip-load
  company-site:dev:ready
  company-site:gamma:ready
  company-site:prod:ready
  app:harbor-kind
  app:clickhouse-kind
  app:postgres-kind
  app:openbao-kind
  ingress:guardianintelligence.org
  ingress:dev.guardianintelligence.org
  ingress:gamma.guardianintelligence.org
  ingress:oci.guardianintelligence.org
  ingress:dashboard.guardianintelligence.org
)

write_evidence_fixture() {
  printf '%s\n' '# Manifest' >"${evidence_dir}/MANIFEST.md"
  printf '%s\n' \
    '# Management Evidence Verification' \
    '- Phase: evidence' \
    '- Mode: evidence' \
    '- Talos required: true' \
    '- Result: PASS' \
    >"${evidence_dir}/VERIFY.md"

  for check in "${required_evidence_checks[@]}"; do
    printf 'pass\t%s\tok\n' "${check}"
  done >"${evidence_dir}/verification.tsv"
}

write_phase_fixture() {
  local node="$1"
  local phase="$2"
  local min_ready="$3"
  local talos_required="$4"
  local phase_dir="${hardware_dir}/${node}/${phase}"

  mkdir -p "${phase_dir}"
  printf '%s\n' '# Manifest' >"${phase_dir}/MANIFEST.md"
  printf '%s\n' \
    '# Management Evidence Verification' \
    "- Phase: ${phase}" \
    '- Mode: outage' \
    "- Minimum Ready nodes: ${min_ready}" \
    "- Talos required: ${talos_required}" \
    '- Result: PASS' \
    >"${phase_dir}/VERIFY.md"
  {
    printf '%s\n' \
      'pass	outage:node-present	ok' \
      'pass	nodes:ready	ok' \
      'pass	company-site:dev:ready	ok' \
      'pass	company-site:gamma:ready	ok' \
      'pass	company-site:prod:ready	ok'
    case "${phase}" in
      outage-before)
        printf '%s\n' 'pass	outage:node-ready-before	ok'
        ;;
      outage-down)
        printf '%s\n' 'pass	outage:node-down	ok'
        ;;
      outage-after)
        printf '%s\n' 'pass	outage:node-ready-after	ok'
        ;;
    esac
  } >"${phase_dir}/verification.tsv"
}

write_node_fixture() {
  local node="$1"
  local node_dir="${hardware_dir}/${node}"

  mkdir -p "${node_dir}"
  printf '%s\n' '# Hardware Node' "- Node: ${node}" >"${node_dir}/MANIFEST.md"
  printf '{"node":"%s","server_id":"sv_test","action":"status","status":"on","timestamp":"2026-06-22T00:00:00Z"}\n' "${node}" >"${node_dir}/latitude-before.jsonl"
  printf '{"node":"%s","server_id":"sv_test","action":"power_off","http_status":202,"timestamp":"2026-06-22T00:00:01Z"}\n' "${node}" >"${node_dir}/latitude-down.jsonl"
  printf '{"node":"%s","server_id":"sv_test","action":"status","status":"off","timestamp":"2026-06-22T00:00:02Z"}\n' "${node}" >>"${node_dir}/latitude-down.jsonl"
  printf '{"node":"%s","server_id":"sv_test","action":"power_on","http_status":202,"timestamp":"2026-06-22T00:00:03Z"}\n' "${node}" >"${node_dir}/latitude-after.jsonl"
  printf '{"node":"%s","server_id":"sv_test","action":"status","status":"on","timestamp":"2026-06-22T00:00:04Z"}\n' "${node}" >>"${node_dir}/latitude-after.jsonl"

  write_phase_fixture "${node}" outage-before 3 true
  write_phase_fixture "${node}" outage-down 2 false
  write_phase_fixture "${node}" outage-after 3 true
}

write_hardware_fixture() {
  printf '%s\n' '# Hardware Outage Evidence Run All' '- Nodes:' '  - ash-earth' '  - ash-wind' '  - ash-water' >"${hardware_dir}/MANIFEST.md"
  write_node_fixture ash-earth
  write_node_fixture ash-wind
  write_node_fixture ash-water
}

write_evidence_fixture
write_hardware_fixture

bash "${script}" \
  --evidence-dir "${evidence_dir}" \
  --hardware-outage-dir "${hardware_dir}" \
  --nodes ash-earth,ash-wind,ash-water \
  --out-dir "${suite_dir}"

grep -Fqx -- '- Result: PASS' "${suite_dir}/SUITE.md"
grep -Fqx -- '- Failures: 0' "${suite_dir}/SUITE.md"
test -s "${suite_dir}/suite-verification.tsv"
printf 'ok\n' >"${out_file}"

#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 2 ]]; then
  echo "usage: verify-management-evidence_test.sh VERIFY_SCRIPT OUT_FILE" >&2
  exit 2
fi

script="$1"
out_file="$2"
tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/management-evidence-fixture.XXXXXX")"
run_dir="${tmpdir}/evidence"
trap 'rm -rf "${tmpdir}"' EXIT

mkdir -p "${run_dir}/kubectl" "${run_dir}/talos" "${run_dir}/evidence"

summary_names=(
  kubectl-version
  kubectl-nodes
  kubectl-storageclasses
  kubectl-pv
  kubectl-pvc
  kubectl-secret-contracts
  kubectl-packages
  kubectl-tenants
  kubectl-backupclasses
  kubectl-plans
  kubectl-backupjobs
  kubectl-backups
  kubectl-apps
  kubectl-company-dev
  kubectl-company-gamma
  kubectl-company-prod
  kubectl-certificates
  kubectl-ingress
  kubectl-pods
  api-vip-load
  talos-health
  talos-etcd-members
  evidence-jobs
  evidence-backupjobs
  evidence-restorejobs
  evidence-restore-verify-jobs
  evidence-restore-targets
  logs-evidence-postgres-load
  logs-evidence-clickhouse-load
  logs-evidence-harbor-oci-read
  logs-evidence-openbao-load
  logs-evidence-http-load
  logs-evidence-storage-smoke
  logs-evidence-postgres-restore-verify
  logs-evidence-clickhouse-restore-verify
)

printf '%s\n' '# Management Evidence Capture' '- Phase: evidence' >"${run_dir}/MANIFEST.md"
: >"${run_dir}/summary.tsv"
for name in "${summary_names[@]}"; do
  printf '%s\t0\tfixture/%s\n' "${name}" "${name}" >>"${run_dir}/summary.tsv"
done

printf '%s\n' \
  'NAME STATUS ROLES AGE VERSION' \
  'ash-earth Ready control-plane 1d v1.34.3' \
  'ash-wind Ready control-plane 1d v1.34.3' \
  'ash-water Ready control-plane 1d v1.34.3' \
  >"${run_dir}/kubectl/nodes-wide.txt"
printf '%s\n' 'replicated' 'replicated-retain' >"${run_dir}/kubectl/storageclasses-wide.txt"
printf '%s\n' 'secret/guardian-r2-db-backups' 'secret/guardian-openbao-evidence-token' >"${run_dir}/kubectl/secret-contracts.txt"
cat >"${run_dir}/kubectl/root-apps.yaml" <<'EOF'
kind: Harbor
metadata:
  name: oci
---
kind: ClickHouse
metadata:
  name: ledger
---
kind: Postgres
metadata:
  name: guardian
---
kind: OpenBAO
metadata:
  name: guardian
EOF

for env in dev gamma prod; do
  printf '%s\n' \
    'deployment.apps/company-site 2/2' \
    'service/company-site ClusterIP' \
    'ingress.networking.k8s.io/company-site' \
    >"${run_dir}/kubectl/company-site-${env}-wide.txt"
done
printf '%s\n' \
  'guardianintelligence.org' \
  'dev.guardianintelligence.org' \
  'gamma.guardianintelligence.org' \
  'oci.guardianintelligence.org' \
  'dashboard.guardianintelligence.org' \
  >"${run_dir}/kubectl/ingress-all-wide.txt"

for rel in \
  kubectl/version.txt \
  kubectl/pv-wide.txt \
  kubectl/pvc-all-wide.txt \
  kubectl/packages-all-wide.txt \
  kubectl/tenants-all-wide.txt \
  kubectl/backupclasses-wide.txt \
  kubectl/plans-all-wide.txt \
  kubectl/backupjobs-all-wide.txt \
  kubectl/backups-all-wide.txt \
  kubectl/certificates-all-wide.txt \
  kubectl/pods-all-wide.txt \
  talos/health.txt \
  talos/etcd-members.txt; do
  printf '%s\n' "${rel}" >"${run_dir}/${rel}"
done

printf '%s\n' \
  'api-vip-load total=60 failures=0 concurrency=6 seconds=1 server=https://10.8.0.250:6443 path=/readyz' \
  >"${run_dir}/evidence/api-vip-load.txt"
printf '%s\n' \
  'evidence-postgres-load 1/1' \
  'evidence-clickhouse-load 1/1' \
  'evidence-harbor-oci-read 1/1' \
  'evidence-openbao-load 1/1' \
  'evidence-http-load 1/1' \
  'evidence-storage-smoke 1/1' \
  >"${run_dir}/evidence/jobs-wide.txt"
cat >"${run_dir}/evidence/backupjobs.yaml" <<'EOF'
metadata:
  name: evidence-postgres-adhoc
status:
  phase: Succeeded
---
metadata:
  name: evidence-clickhouse-adhoc
status:
  phase: Succeeded
EOF
cat >"${run_dir}/evidence/restorejobs.yaml" <<'EOF'
metadata:
  name: evidence-postgres-to-copy
status:
  phase: Succeeded
---
metadata:
  name: evidence-clickhouse-to-copy
status:
  phase: Succeeded
EOF
printf '%s\n' \
  'evidence-postgres-restore-verify 1/1' \
  'evidence-clickhouse-restore-verify 1/1' \
  >"${run_dir}/evidence/restore-verify-jobs-wide.txt"
printf '%s\n' 'guardian-restore-check Ready' 'ledger-restore-check Ready' >"${run_dir}/evidence/restore-targets-wide.txt"
printf '%s\n' 'postgres-load run_id=test workers=4 rows_per_worker=250 expected=1000 actual=1000' >"${run_dir}/evidence/logs-evidence-postgres-load.txt"
printf '%s\n' 'clickhouse-load run_id=test workers=4 rows_per_worker=250 expected=1000 actual=1000 cluster_rows=3' >"${run_dir}/evidence/logs-evidence-clickhouse-load.txt"
printf '%s\n' 'harbor-oci-read total=25 failures=0 repository=guardian/company-site digest=sha256:test' >"${run_dir}/evidence/logs-evidence-harbor-oci-read.txt"
printf '%s\n' 'openbao-load total=25 failures=0 run_id=test' >"${run_dir}/evidence/logs-evidence-openbao-load.txt"
{
  printf '%s\n' \
    'http-target label=company-prod-root url=https://guardianintelligence.org/ total=100 failures=0' \
    'http-target label=company-prod-letters url=https://guardianintelligence.org/letters/ total=100 failures=0' \
    'http-target label=company-prod-news url=https://guardianintelligence.org/news/ total=100 failures=0' \
    'http-target label=company-prod-healthz url=https://guardianintelligence.org/healthz total=100 failures=0' \
    'http-target label=company-prod-metrics url=https://guardianintelligence.org/metrics total=100 failures=0' \
    'http-target label=company-dev-root url=https://dev.guardianintelligence.org/ total=100 failures=0' \
    'http-target label=company-dev-letters url=https://dev.guardianintelligence.org/letters/ total=100 failures=0' \
    'http-target label=company-dev-news url=https://dev.guardianintelligence.org/news/ total=100 failures=0' \
    'http-target label=company-dev-healthz url=https://dev.guardianintelligence.org/healthz total=100 failures=0' \
    'http-target label=company-dev-metrics url=https://dev.guardianintelligence.org/metrics total=100 failures=0' \
    'http-target label=company-gamma-root url=https://gamma.guardianintelligence.org/ total=100 failures=0' \
    'http-target label=company-gamma-letters url=https://gamma.guardianintelligence.org/letters/ total=100 failures=0' \
    'http-target label=company-gamma-news url=https://gamma.guardianintelligence.org/news/ total=100 failures=0' \
    'http-target label=company-gamma-healthz url=https://gamma.guardianintelligence.org/healthz total=100 failures=0' \
    'http-target label=company-gamma-metrics url=https://gamma.guardianintelligence.org/metrics total=100 failures=0' \
    'http-target label=harbor-health url=https://oci.guardianintelligence.org/api/v2.0/health total=100 failures=0' \
    'http-target label=dashboard-root url=https://dashboard.guardianintelligence.org/ total=100 failures=0' \
    'http-load total=1700 failures=0'
} >"${run_dir}/evidence/logs-evidence-http-load.txt"
printf '%s\n' 'storage-smoke files=64 marker=/data/.guardian-evidence.sha256' >"${run_dir}/evidence/logs-evidence-storage-smoke.txt"
printf '%s\n' 'postgres-restore-verify expected_minimum=1000 actual=1000 attempts=1' >"${run_dir}/evidence/logs-evidence-postgres-restore-verify.txt"
printf '%s\n' 'clickhouse-restore-verify expected_minimum=1000 actual=1000 attempts=1' >"${run_dir}/evidence/logs-evidence-clickhouse-restore-verify.txt"

bash "${script}" --run-dir "${run_dir}" --mode evidence --require-talos

grep -Fqx -- '- Result: PASS' "${run_dir}/VERIFY.md"
grep -Fqx -- '- Failures: 0' "${run_dir}/VERIFY.md"
awk -F '\t' '$1 == "pass" && $2 == "api:vip-load" {found = 1} END {exit found ? 0 : 1}' "${run_dir}/verification.tsv"

run_outage_fixture() {
  local phase="$1"
  local node_status="$2"
  local min_ready_nodes="$3"
  local expected_check="$4"
  local require_talos="$5"
  local args=(
    --run-dir "${run_dir}"
    --mode outage
    --phase "${phase}"
    --node ash-earth
    --min-ready-nodes "${min_ready_nodes}"
  )

  printf '%s\n' '# Management Evidence Capture' "- Phase: ${phase}" >"${run_dir}/MANIFEST.md"
  printf '%s\n' \
    'NAME STATUS ROLES AGE VERSION' \
    "ash-earth ${node_status} control-plane 1d v1.34.3" \
    'ash-wind Ready control-plane 1d v1.34.3' \
    'ash-water Ready control-plane 1d v1.34.3' \
    >"${run_dir}/kubectl/nodes-wide.txt"

  if [[ "${require_talos}" == "true" ]]; then
    args+=(--require-talos)
  fi
  bash "${script}" "${args[@]}"

  grep -Fqx -- '- Result: PASS' "${run_dir}/VERIFY.md"
  grep -Fqx -- '- Failures: 0' "${run_dir}/VERIFY.md"
  awk -F '\t' -v expected_check="${expected_check}" '$1 == "pass" && $2 == expected_check {found = 1} END {exit found ? 0 : 1}' "${run_dir}/verification.tsv"
}

run_outage_fixture outage-before Ready 3 outage:node-ready-before true
run_outage_fixture outage-down NotReady 2 outage:node-down false
run_outage_fixture outage-after Ready 3 outage:node-ready-after true

printf 'ok\n' >"${out_file}"

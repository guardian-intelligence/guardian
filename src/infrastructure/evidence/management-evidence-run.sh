#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: management-evidence-run [OPTIONS]

Runs the full live management-cluster evidence suite:
  1. check live base app, secret, and tenant surface prerequisites;
  2. clean and apply the opt-in evidence overlay;
  3. wait for load jobs and database BackupJobs;
  4. apply and wait for database RestoreJobs;
  5. capture and verify load/DR evidence with Talos required;
  6. run all-node Latitude hardware outage evidence with component probes;
  7. verify the complete evidence package.

This command performs live Kubernetes writes and powers off each selected
Latitude management node sequentially.

Options:
  --out-dir DIR           parent output directory; defaults under live-runs/
  --kubeconfig PATH       optional kubeconfig path
  --context NAME          optional kubeconfig context
  --talosconfig PATH      optional talosconfig path; TALOSCONFIG env also works
  --talos-endpoints LIST  Talos endpoint list; default 10.8.0.250
  --talos-nodes LIST      Talos node list; default 10.8.0.11,10.8.0.12,10.8.0.13
  --api-server URL        Kubernetes API VIP URL; default https://10.8.0.250:6443
  --api-load-requests N   API VIP read count for evidence capture; default 60
  --api-load-concurrency N API VIP concurrent read workers; default 6
  --timeout DURATION      evidence job/backup/restore wait timeout; default 30m
  --inventory PATH        inventory JSON; defaults to guardian-mgmt inventory
  --nodes CSV             optional management node subset; defaults to inventory
  --down-timeout DURATION Latitude status wait after power_off; default 10m
  --up-timeout DURATION   Latitude status wait after power_on; default 15m
  --poll-interval DURATION Latitude status poll interval; default 10s
  --probe-timeout DURATION component probe Job wait timeout; default 10m
  --skip-component-probes skip per-component load probes in outage phases
  --delete-pvc            also delete retained evidence storage PVC before run
  -h, --help              show this help

Inputs:
  KUBECTL_BIN                repo-pinned kubectl executable path
  TALOSCTL_BIN               repo-pinned talosctl executable path
  LATITUDE_POWER_BIN         repo-pinned Latitude power helper
  MANAGEMENT_INVENTORY_BIN   repo-pinned inventory reader
  CAPTURE_EVIDENCE_BIN       repo-pinned capture helper
  VERIFY_EVIDENCE_BIN        repo-pinned phase verifier
  HARDWARE_OUTAGE_BIN        repo-pinned per-node outage runner
  HARDWARE_OUTAGE_ALL_BIN    repo-pinned all-node outage runner
  VERIFY_EVIDENCE_SUITE_BIN  repo-pinned suite verifier
  LATITUDESH_AUTH_TOKEN      Latitude API token; LATITUDESH_BEARER also works
EOF
}

out_dir=""
kubeconfig=""
context=""
talosconfig="${TALOSCONFIG:-}"
talos_endpoints="10.8.0.250"
talos_nodes="10.8.0.11,10.8.0.12,10.8.0.13"
api_server="https://10.8.0.250:6443"
api_load_requests="60"
api_load_concurrency="6"
timeout="30m"
inventory="src/infrastructure/inventory/guardian-mgmt.json"
nodes_csv=""
down_timeout="10m"
up_timeout="15m"
poll_interval="10s"
probe_timeout="10m"
component_probes=true
delete_pvc=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --out-dir)
      out_dir="${2:?--out-dir requires a value}"
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
    --timeout)
      timeout="${2:?--timeout requires a value}"
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
    --down-timeout)
      down_timeout="${2:?--down-timeout requires a value}"
      shift 2
      ;;
    --up-timeout)
      up_timeout="${2:?--up-timeout requires a value}"
      shift 2
      ;;
    --poll-interval)
      poll_interval="${2:?--poll-interval requires a value}"
      shift 2
      ;;
    --probe-timeout)
      probe_timeout="${2:?--probe-timeout requires a value}"
      shift 2
      ;;
    --skip-component-probes)
      component_probes=false
      shift
      ;;
    --delete-pvc)
      delete_pvc=true
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
latitude_power_bin="${LATITUDE_POWER_BIN:-}"
management_inventory_bin="${MANAGEMENT_INVENTORY_BIN:-}"
capture_evidence_bin="${CAPTURE_EVIDENCE_BIN:-}"
verify_evidence_bin="${VERIFY_EVIDENCE_BIN:-}"
hardware_outage_bin="${HARDWARE_OUTAGE_BIN:-}"
hardware_outage_all_bin="${HARDWARE_OUTAGE_ALL_BIN:-}"
verify_evidence_suite_bin="${VERIFY_EVIDENCE_SUITE_BIN:-}"
missing=()

[[ -n "${kubectl_bin}" && -x "${kubectl_bin}" ]] || missing+=("executable KUBECTL_BIN")
[[ -n "${talosctl_bin}" && -x "${talosctl_bin}" ]] || missing+=("executable TALOSCTL_BIN")
[[ -n "${latitude_power_bin}" && -x "${latitude_power_bin}" ]] || missing+=("executable LATITUDE_POWER_BIN")
[[ -n "${management_inventory_bin}" && -x "${management_inventory_bin}" ]] || missing+=("executable MANAGEMENT_INVENTORY_BIN")
[[ -n "${capture_evidence_bin}" && -x "${capture_evidence_bin}" ]] || missing+=("executable CAPTURE_EVIDENCE_BIN")
[[ -n "${verify_evidence_bin}" && -x "${verify_evidence_bin}" ]] || missing+=("executable VERIFY_EVIDENCE_BIN")
[[ -n "${hardware_outage_bin}" && -x "${hardware_outage_bin}" ]] || missing+=("executable HARDWARE_OUTAGE_BIN")
[[ -n "${hardware_outage_all_bin}" && -x "${hardware_outage_all_bin}" ]] || missing+=("executable HARDWARE_OUTAGE_ALL_BIN")
[[ -n "${verify_evidence_suite_bin}" && -x "${verify_evidence_suite_bin}" ]] || missing+=("executable VERIFY_EVIDENCE_SUITE_BIN")
[[ -n "${talosconfig}" ]] || missing+=("--talosconfig or TALOSCONFIG; final suite verification requires Talos captures")
if [[ ! "${api_load_requests}" =~ ^[1-9][0-9]*$ ]]; then
  missing+=("positive integer --api-load-requests")
fi
if [[ ! "${api_load_concurrency}" =~ ^[1-9][0-9]*$ ]]; then
  missing+=("positive integer --api-load-concurrency")
fi
if [[ -z "${LATITUDESH_AUTH_TOKEN:-}" && -z "${LATITUDESH_BEARER:-}" ]]; then
  missing+=("LATITUDESH_AUTH_TOKEN or LATITUDESH_BEARER")
fi
if [[ "${#missing[@]}" -gt 0 ]]; then
  echo "missing required inputs:" >&2
  printf '  - %s\n' "${missing[@]}" >&2
  exit 1
fi

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
if [[ -z "${out_dir}" ]]; then
  out_dir="docs/reports/infrastructure/live-runs/${timestamp}-management-evidence"
fi
evidence_dir="${out_dir}/evidence"
hardware_outage_dir="${out_dir}/hardware-outage-all"
suite_dir="${out_dir}/management-suite"
mkdir -p "${out_dir}"

kubectl_args=()
if [[ -n "${kubeconfig}" ]]; then
  kubectl_args+=(--kubeconfig "${kubeconfig}")
fi
if [[ -n "${context}" ]]; then
  kubectl_args+=(--context "${context}")
fi

capture_args=()
if [[ -n "${kubeconfig}" ]]; then
  capture_args+=(--kubeconfig "${kubeconfig}")
fi
if [[ -n "${context}" ]]; then
  capture_args+=(--context "${context}")
fi
capture_args+=(--talosconfig "${talosconfig}")
capture_args+=(--talos-endpoints "${talos_endpoints}")
capture_args+=(--talos-nodes "${talos_nodes}")
capture_args+=(--api-server "${api_server}")
capture_args+=(--api-load-requests "${api_load_requests}")
capture_args+=(--api-load-concurrency "${api_load_concurrency}")

hardware_args=()
if [[ -n "${kubeconfig}" ]]; then
  hardware_args+=(--kubeconfig "${kubeconfig}")
fi
if [[ -n "${context}" ]]; then
  hardware_args+=(--context "${context}")
fi
hardware_args+=(--talosconfig "${talosconfig}")
hardware_args+=(--talos-endpoints "${talos_endpoints}")
hardware_args+=(--talos-nodes "${talos_nodes}")
hardware_args+=(--inventory "${inventory}")
hardware_args+=(--down-timeout "${down_timeout}")
hardware_args+=(--up-timeout "${up_timeout}")
hardware_args+=(--poll-interval "${poll_interval}")
hardware_args+=(--probe-timeout "${probe_timeout}")
hardware_args+=(--require-talos)
if [[ -n "${nodes_csv}" ]]; then
  hardware_args+=(--nodes "${nodes_csv}")
fi
if [[ "${component_probes}" != "true" ]]; then
  hardware_args+=(--skip-component-probes)
fi

suite_args=(
  --evidence-dir "${evidence_dir}"
  --hardware-outage-dir "${hardware_outage_dir}"
  --inventory "${inventory}"
  --out-dir "${suite_dir}"
)
if [[ -n "${nodes_csv}" ]]; then
  suite_args+=(--nodes "${nodes_csv}")
fi

kubectl_cmd() {
  "${kubectl_bin}" "${kubectl_args[@]}" "$@"
}

capture_failed() {
  local failed_dir="${out_dir}/evidence-failed"
  echo "capturing degraded evidence to ${failed_dir}" >&2
  "${capture_evidence_bin}" \
    --out-dir "${failed_dir}" \
    --phase evidence-failed \
    --allow-failures \
    "${capture_args[@]}" || true
}

run_step() {
  local name="$1"
  shift

  echo "running ${name}"
  if "$@"; then
    echo "completed ${name}"
    return 0
  fi
  local rc=$?
  echo "management evidence run failed during ${name}" >&2
  capture_failed
  return "${rc}"
}

evidence_clean() {
  kubectl_cmd delete job/evidence-postgres-load job/evidence-clickhouse-load job/evidence-harbor-oci-read job/evidence-openbao-load job/evidence-http-load job/evidence-storage-smoke job/evidence-postgres-restore-verify job/evidence-clickhouse-restore-verify -n tenant-root --ignore-not-found
  kubectl_cmd delete backupjob/evidence-postgres-adhoc backupjob/evidence-clickhouse-adhoc -n tenant-root --ignore-not-found
  kubectl_cmd delete restorejob/evidence-postgres-to-copy restorejob/evidence-clickhouse-to-copy -n tenant-root --ignore-not-found
  kubectl_cmd delete postgres/guardian-restore-check clickhouse/ledger-restore-check -n tenant-root --ignore-not-found
  kubectl_cmd delete configmap/evidence-postgres-load configmap/evidence-clickhouse-load configmap/evidence-harbor-oci-read configmap/evidence-openbao-load configmap/evidence-http-load configmap/evidence-storage-smoke configmap/evidence-postgres-restore-verify configmap/evidence-clickhouse-restore-verify -n tenant-root --ignore-not-found
  if [[ "${delete_pvc}" == "true" ]]; then
    kubectl_cmd delete pvc/evidence-replicated-retain -n tenant-root --ignore-not-found
  fi
}

evidence_apply() {
  kubectl_cmd apply -k src/infrastructure/evidence
}

evidence_prerequisites() {
  kubectl_cmd get secret/guardian-r2-db-backups secret/guardian-openbao-evidence-token -n tenant-root -o name
  kubectl_cmd get harbor/oci clickhouse/ledger postgres/guardian openbao/guardian -n tenant-root -o name

  local namespace
  for namespace in tenant-dev tenant-gamma tenant-root; do
    kubectl_cmd get deploy/company-site svc/company-site ing/company-site -n "${namespace}" -o name
  done
}

evidence_wait() {
  local job
  for job in \
    evidence-postgres-load \
    evidence-clickhouse-load \
    evidence-harbor-oci-read \
    evidence-openbao-load \
    evidence-http-load \
    evidence-storage-smoke; do
    kubectl_cmd wait "job/${job}" -n tenant-root --for=condition=complete --timeout "${timeout}"
  done

  local backupjob
  for backupjob in evidence-postgres-adhoc evidence-clickhouse-adhoc; do
    kubectl_cmd wait "backupjob/${backupjob}" -n tenant-root '--for=jsonpath={.status.phase}=Succeeded' --timeout "${timeout}"
  done
}

evidence_restore_apply() {
  kubectl_cmd apply -f src/infrastructure/evidence/database-restore.yaml
}

evidence_restore_wait() {
  local restorejob
  for restorejob in evidence-postgres-to-copy evidence-clickhouse-to-copy; do
    kubectl_cmd wait "restorejob/${restorejob}" -n tenant-root '--for=jsonpath={.status.phase}=Succeeded' --timeout "${timeout}"
  done

  local job
  for job in evidence-postgres-restore-verify evidence-clickhouse-restore-verify; do
    kubectl_cmd wait "job/${job}" -n tenant-root --for=condition=complete --timeout "${timeout}"
  done
}

evidence_capture() {
  "${capture_evidence_bin}" \
    --out-dir "${evidence_dir}" \
    --phase evidence \
    "${capture_args[@]}"
}

evidence_verify() {
  "${verify_evidence_bin}" \
    --run-dir "${evidence_dir}" \
    --mode evidence \
    --require-talos
}

hardware_outage_all() {
  "${hardware_outage_all_bin}" \
    --out-dir "${hardware_outage_dir}" \
    "${hardware_args[@]}"
}

suite_verify() {
  "${verify_evidence_suite_bin}" "${suite_args[@]}"
}

cat >"${out_dir}/MANIFEST.md" <<EOF
# Management Evidence Run

- Captured at: ${timestamp}
- Output directory: ${out_dir}
- Evidence capture: evidence
- Hardware outage capture: hardware-outage-all
- Suite verification: management-suite
- Inventory: ${inventory}
- Talos endpoint list: ${talos_endpoints}
- Talos node list: ${talos_nodes}
- API server: ${api_server}
- API load requests: ${api_load_requests}
- API load concurrency: ${api_load_concurrency}
- Evidence timeout: ${timeout}
- Latitude down timeout: ${down_timeout}
- Latitude up timeout: ${up_timeout}
- Latitude poll interval: ${poll_interval}
- Component probes during outages: ${component_probes}
- Component probe timeout: ${probe_timeout}
- Delete retained evidence PVC: ${delete_pvc}

This run performs live Kubernetes writes and sequential Latitude hardware power
actions for each selected management node.
EOF

run_step "evidence-prerequisites" evidence_prerequisites
run_step "evidence-clean" evidence_clean
run_step "evidence-apply" evidence_apply
run_step "evidence-wait" evidence_wait
run_step "evidence-restore-apply" evidence_restore_apply
run_step "evidence-restore-wait" evidence_restore_wait
run_step "evidence-capture" evidence_capture
run_step "evidence-verify" evidence_verify
run_step "hardware-outage-run-all" hardware_outage_all
run_step "evidence-verify-suite" suite_verify

echo "wrote management evidence run to ${out_dir}"

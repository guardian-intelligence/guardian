#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: hardware-outage-run --node NAME [OPTIONS]

Runs a true hardware single-node outage rehearsal:
  1. rerun component probes, then capture and verify outage-before;
  2. power off the Latitude server and wait for status=off;
  3. rerun component probes, then capture and verify outage-down with two Ready nodes required;
  4. power on the server and wait for status=on;
  5. rerun component probes, then capture and verify outage-after.

Options:
  --node NAME             management node name from guardian-mgmt inventory
  --inventory PATH        inventory JSON; defaults to guardian-mgmt inventory
  --out-dir DIR           output directory; defaults under live-runs/
  --kubeconfig PATH       optional kubeconfig path
  --context NAME          optional kubeconfig context
  --talosconfig PATH      optional talosconfig path
  --talos-endpoints LIST  Talos endpoint list
  --talos-nodes LIST      Talos node list
  --down-timeout DURATION Latitude status wait after power_off; default 10m
  --up-timeout DURATION   Latitude status wait after power_on; default 15m
  --poll-interval DURATION Latitude status poll interval; default 10s
  --probe-timeout DURATION component probe Job wait timeout; default 10m
  --require-talos         require Talos capture success for before/after
  --skip-component-probes skip per-component load probes in outage phases
  -h, --help              show this help

Inputs:
  LATITUDE_POWER_BIN      repo-pinned Latitude power helper
  CAPTURE_EVIDENCE_BIN   repo-pinned evidence capture helper
  VERIFY_EVIDENCE_BIN    repo-pinned evidence verifier
  KUBECTL_BIN            repo-pinned kubectl executable path
  TALOSCTL_BIN           repo-pinned talosctl executable path
  LATITUDESH_AUTH_TOKEN  Latitude API token; LATITUDESH_BEARER also works
EOF
}

node=""
inventory="src/infrastructure/inventory/guardian-mgmt.json"
out_dir=""
kubeconfig=""
context=""
talosconfig="${TALOSCONFIG:-}"
talos_endpoints="10.8.0.250"
talos_nodes="10.8.0.11,10.8.0.12,10.8.0.13"
down_timeout="10m"
up_timeout="15m"
poll_interval="10s"
probe_timeout="10m"
require_talos=false
component_probes=true

while [[ $# -gt 0 ]]; do
  case "$1" in
    --node)
      node="${2:?--node requires a value}"
      shift 2
      ;;
    --inventory)
      inventory="${2:?--inventory requires a value}"
      shift 2
      ;;
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
    --require-talos)
      require_talos=true
      shift
      ;;
    --skip-component-probes)
      component_probes=false
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

if [[ -z "${node}" ]]; then
  echo "ERROR: pass --node with the management node under hardware outage" >&2
  exit 2
fi

latitude_power_bin="${LATITUDE_POWER_BIN:-}"
capture_evidence_bin="${CAPTURE_EVIDENCE_BIN:-}"
verify_evidence_bin="${VERIFY_EVIDENCE_BIN:-}"
kubectl_bin="${KUBECTL_BIN:-}"
talosctl_bin="${TALOSCTL_BIN:-}"
missing=()
[[ -n "${latitude_power_bin}" && -x "${latitude_power_bin}" ]] || missing+=("executable LATITUDE_POWER_BIN")
[[ -n "${capture_evidence_bin}" && -x "${capture_evidence_bin}" ]] || missing+=("executable CAPTURE_EVIDENCE_BIN")
[[ -n "${verify_evidence_bin}" && -x "${verify_evidence_bin}" ]] || missing+=("executable VERIFY_EVIDENCE_BIN")
[[ -n "${kubectl_bin}" && -x "${kubectl_bin}" ]] || missing+=("executable KUBECTL_BIN")
[[ -n "${talosctl_bin}" && -x "${talosctl_bin}" ]] || missing+=("executable TALOSCTL_BIN")
if [[ -z "${LATITUDESH_AUTH_TOKEN:-}" && -z "${LATITUDESH_BEARER:-}" ]]; then
  missing+=("LATITUDESH_AUTH_TOKEN or LATITUDESH_BEARER")
fi
if [[ "${#missing[@]}" -gt 0 ]]; then
  echo "missing required inputs:" >&2
  printf '  - %s\n' "${missing[@]}" >&2
  exit 1
fi

safe_node="$(printf '%s' "${node}" | tr -c 'A-Za-z0-9_.-' '-')"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
if [[ -z "${out_dir}" ]]; then
  out_dir="docs/reports/infrastructure/live-runs/${timestamp}-hardware-outage-${safe_node}"
fi
mkdir -p "${out_dir}"
power_restore_required=false
kubectl_args=()
if [[ -n "${kubeconfig}" ]]; then
  kubectl_args+=(--kubeconfig "${kubeconfig}")
fi
if [[ -n "${context}" ]]; then
  kubectl_args+=(--context "${context}")
fi

run_latitude() {
  local action="$1"
  local timeout="$2"
  local output="$3"

  "${latitude_power_bin}" \
    --inventory "${inventory}" \
    --node "${node}" \
    --action "${action}" \
    --timeout "${timeout}" \
    --poll-interval "${poll_interval}" \
    >"${output}"
}

kubectl_cmd() {
  "${kubectl_bin}" "${kubectl_args[@]}" "$@"
}

run_component_probes() {
  if [[ "${component_probes}" != "true" ]]; then
    return 0
  fi

  local job
  kubectl_cmd delete \
    job/evidence-postgres-load \
    job/evidence-clickhouse-load \
    job/evidence-harbor-oci-read \
    job/evidence-openbao-load \
    job/evidence-http-load \
    job/evidence-storage-smoke \
    -n tenant-root \
    --ignore-not-found

  kubectl_cmd apply -f src/infrastructure/evidence/database-load.yaml
  kubectl_cmd apply -f src/infrastructure/evidence/harbor-oci-read.yaml
  kubectl_cmd apply -f src/infrastructure/evidence/http-load.yaml
  kubectl_cmd apply -f src/infrastructure/evidence/openbao-load.yaml
  kubectl_cmd apply -f src/infrastructure/evidence/storage-smoke.yaml

  for job in \
    evidence-postgres-load \
    evidence-clickhouse-load \
    evidence-harbor-oci-read \
    evidence-openbao-load \
    evidence-http-load \
    evidence-storage-smoke; do
    kubectl_cmd wait "job/${job}" -n tenant-root --for=condition=complete --timeout "${probe_timeout}"
  done
}

restore_power_on_exit() {
  local rc=$?

  if [[ "${power_restore_required}" == "true" ]]; then
    echo "attempting Latitude power_on for ${node} before exit" >&2
    set +e
    run_latitude power_on "${up_timeout}" "${out_dir}/latitude-after.jsonl"
    local restore_rc=$?
    set -e
    if [[ "${restore_rc}" -ne 0 ]]; then
      echo "Latitude power_on for ${node} failed during exit cleanup" >&2
      if [[ "${rc}" -eq 0 ]]; then
        rc="${restore_rc}"
      fi
    fi
  fi

  exit "${rc}"
}

trap restore_power_on_exit EXIT

capture_args() {
  if [[ -n "${kubeconfig}" ]]; then
    printf '%s\n' --kubeconfig "${kubeconfig}"
  fi
  if [[ -n "${context}" ]]; then
    printf '%s\n' --context "${context}"
  fi
  if [[ -n "${talosconfig}" ]]; then
    printf '%s\n' --talosconfig "${talosconfig}"
  fi
  if [[ -n "${talos_endpoints}" ]]; then
    printf '%s\n' --talos-endpoints "${talos_endpoints}"
  fi
  if [[ -n "${talos_nodes}" ]]; then
    printf '%s\n' --talos-nodes "${talos_nodes}"
  fi
}

run_capture() {
  local phase="$1"
  local allow_failures="$2"
  local phase_dir="${out_dir}/${phase}"

  mkdir -p "${phase_dir}"
  mapfile -t args < <(capture_args)
  if [[ "${allow_failures}" == "true" ]]; then
    args+=(--allow-failures)
  fi
  "${capture_evidence_bin}" \
    --out-dir "${phase_dir}" \
    --phase "${phase}" \
    "${args[@]}"
}

run_or_capture_failure() {
  local failed_phase="$1"
  shift
  local rc=0

  "$@" || rc=$?
  if [[ "${rc}" -eq 0 ]]; then
    return 0
  fi

  echo "hardware outage step failed; capturing ${failed_phase}" >&2
  run_capture "${failed_phase}" true || true
  return "${rc}"
}

run_verify() {
  local phase="$1"
  local min_ready_nodes="$2"
  local require_talos_for_phase="$3"
  local phase_dir="${out_dir}/${phase}"
  local args=(
    --run-dir "${phase_dir}"
    --phase "${phase}"
    --mode outage
    --node "${node}"
    --min-ready-nodes "${min_ready_nodes}"
  )
  if [[ "${require_talos_for_phase}" == "true" ]]; then
    args+=(--require-talos)
  fi
  if [[ "${component_probes}" == "true" ]]; then
    args+=(--require-component-probes)
  fi
  "${verify_evidence_bin}" "${args[@]}"
}

write_manifest() {
  cat >"${out_dir}/MANIFEST.md" <<EOF
# Hardware Outage Evidence Run

- Node: ${node}
- Inventory: ${inventory}
- Captured at: ${timestamp}
- Output directory: ${out_dir}
- Latitude before: latitude-before.jsonl
- Latitude power off and down status: latitude-down.jsonl
- Latitude power on and after status: latitude-after.jsonl
- Kubernetes/Talos phases: outage-before, outage-down, outage-after
- Component probes: ${component_probes}
EOF
}

write_manifest
run_latitude status "1s" "${out_dir}/latitude-before.jsonl"
run_or_capture_failure outage-before-failed run_component_probes
run_or_capture_failure outage-before-failed run_capture outage-before false
run_or_capture_failure outage-before-failed run_verify outage-before 3 "${require_talos}"

power_restore_required=true
run_latitude power_off "${down_timeout}" "${out_dir}/latitude-down.jsonl"
run_or_capture_failure outage-down-failed run_component_probes
run_or_capture_failure outage-down-failed run_capture outage-down true
run_or_capture_failure outage-down-failed run_verify outage-down 2 false

run_latitude power_on "${up_timeout}" "${out_dir}/latitude-after.jsonl"
power_restore_required=false
run_or_capture_failure outage-after-failed run_component_probes
run_or_capture_failure outage-after-failed run_capture outage-after false
run_or_capture_failure outage-after-failed run_verify outage-after 3 "${require_talos}"

echo "wrote hardware outage evidence to ${out_dir}"

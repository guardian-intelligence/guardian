#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: hardware-outage-run-all [OPTIONS]

Runs hardware-outage-run once for every management node in the checked-in
inventory, sequentially. This is the report path for proving single-node outage
recovery across the whole management control plane. Each per-node run reruns
component probes before, during, and after the power cycle unless explicitly
skipped.

Options:
  --nodes CSV             optional comma-separated node subset; defaults to inventory
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
  MANAGEMENT_INVENTORY_BIN repo-pinned inventory reader
  HARDWARE_OUTAGE_BIN      repo-pinned per-node hardware outage runner
  LATITUDESH_AUTH_TOKEN    Latitude API token; LATITUDESH_BEARER also works
EOF
}

nodes_csv=""
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
    --nodes)
      nodes_csv="${2:?--nodes requires a value}"
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

management_inventory_bin="${MANAGEMENT_INVENTORY_BIN:-}"
hardware_outage_bin="${HARDWARE_OUTAGE_BIN:-}"
missing=()
[[ -n "${management_inventory_bin}" && -x "${management_inventory_bin}" ]] || missing+=("executable MANAGEMENT_INVENTORY_BIN")
[[ -n "${hardware_outage_bin}" && -x "${hardware_outage_bin}" ]] || missing+=("executable HARDWARE_OUTAGE_BIN")
if [[ "${#missing[@]}" -gt 0 ]]; then
  echo "missing required inputs:" >&2
  printf '  - %s\n' "${missing[@]}" >&2
  exit 1
fi

nodes=()
if [[ -n "${nodes_csv}" ]]; then
  IFS=',' read -r -a nodes <<<"${nodes_csv}"
else
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
  out_dir="docs/reports/infrastructure/live-runs/${timestamp}-hardware-outage-all"
fi
mkdir -p "${out_dir}"

common_args=(
  --inventory "${inventory}"
  --down-timeout "${down_timeout}"
  --up-timeout "${up_timeout}"
  --poll-interval "${poll_interval}"
  --probe-timeout "${probe_timeout}"
)
if [[ -n "${kubeconfig}" ]]; then
  common_args+=(--kubeconfig "${kubeconfig}")
fi
if [[ -n "${context}" ]]; then
  common_args+=(--context "${context}")
fi
if [[ -n "${talosconfig}" ]]; then
  common_args+=(--talosconfig "${talosconfig}")
fi
if [[ -n "${talos_endpoints}" ]]; then
  common_args+=(--talos-endpoints "${talos_endpoints}")
fi
if [[ -n "${talos_nodes}" ]]; then
  common_args+=(--talos-nodes "${talos_nodes}")
fi
if [[ "${require_talos}" == "true" ]]; then
  common_args+=(--require-talos)
fi
if [[ "${component_probes}" != "true" ]]; then
  common_args+=(--skip-component-probes)
fi

{
  echo "# Hardware Outage Evidence Run All"
  echo
  echo "- Inventory: ${inventory}"
  echo "- Captured at: ${timestamp}"
  echo "- Output directory: ${out_dir}"
  echo "- Component probes: ${component_probes}"
  echo "- Nodes:"
  for node in "${nodes[@]}"; do
    echo "  - ${node}"
  done
} >"${out_dir}/MANIFEST.md"

for node in "${nodes[@]}"; do
  node_dir="${out_dir}/${node}"
  echo "running hardware outage evidence for ${node}"
  "${hardware_outage_bin}" \
    --node "${node}" \
    --out-dir "${node_dir}" \
    "${common_args[@]}"
done

echo "wrote all-node hardware outage evidence to ${out_dir}"

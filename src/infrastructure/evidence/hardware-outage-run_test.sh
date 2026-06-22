#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 2 ]]; then
  echo "usage: hardware-outage-run_test.sh HARDWARE_OUTAGE_SCRIPT OUT_FILE" >&2
  exit 2
fi

script="$1"
out_file="$2"
tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/hardware-outage-fixture.XXXXXX")"
trap 'rm -rf "${tmpdir}"' EXIT

action_log="${tmpdir}/latitude-actions.log"
fake_latitude="${tmpdir}/latitude-power"
fake_capture="${tmpdir}/capture"
fake_verify="${tmpdir}/verify"
fake_kubectl="${tmpdir}/kubectl"
fake_talosctl="${tmpdir}/talosctl"

cat >"${fake_latitude}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

node=""
action=""
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --node)
      node="$2"
      shift 2
      ;;
    --action)
      action="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

printf '%s\n' "${action}" >>"${LATITUDE_ACTION_LOG}"
case "${action}" in
  status)
    printf '{"node":"%s","action":"status","status":"on"}\n' "${node}"
    ;;
  power_off)
    printf '{"node":"%s","action":"power_off","http_status":202}\n' "${node}"
    printf '{"node":"%s","action":"status","status":"off"}\n' "${node}"
    ;;
  power_on)
    printf '{"node":"%s","action":"power_on","http_status":202}\n' "${node}"
    printf '{"node":"%s","action":"status","status":"on"}\n' "${node}"
    ;;
  *)
    echo "unexpected action: ${action}" >&2
    exit 1
    ;;
esac
EOF

cat >"${fake_capture}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

phase=""
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --phase)
      phase="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

if [[ "${phase}" == "outage-down" ]]; then
  echo "simulated outage-down capture failure" >&2
  exit 7
fi
EOF

cat >"${fake_verify}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
exit 0
EOF

cat >"${fake_kubectl}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
exit 0
EOF

cat >"${fake_talosctl}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
exit 0
EOF

chmod +x "${fake_latitude}" "${fake_capture}" "${fake_verify}" "${fake_kubectl}" "${fake_talosctl}"

set +e
LATITUDE_ACTION_LOG="${action_log}" \
LATITUDESH_AUTH_TOKEN="test-token" \
LATITUDE_POWER_BIN="${fake_latitude}" \
CAPTURE_EVIDENCE_BIN="${fake_capture}" \
VERIFY_EVIDENCE_BIN="${fake_verify}" \
KUBECTL_BIN="${fake_kubectl}" \
TALOSCTL_BIN="${fake_talosctl}" \
bash "${script}" \
  --node ash-earth \
  --out-dir "${tmpdir}/run" \
  --inventory "${tmpdir}/inventory.json" \
  --talosconfig "${tmpdir}/talosconfig" \
  >"${tmpdir}/stdout.log" \
  2>"${tmpdir}/stderr.log"
rc=$?
set -e

if [[ "${rc}" -eq 0 ]]; then
  echo "hardware outage runner unexpectedly succeeded" >&2
  exit 1
fi

grep -Fqx status "${action_log}"
grep -Fqx power_off "${action_log}"
grep -Fqx power_on "${action_log}"

awk '
  $0 == "power_off" {saw_off = NR}
  $0 == "power_on" {saw_on = NR}
  END {exit saw_off > 0 && saw_on > saw_off ? 0 : 1}
' "${action_log}"

grep -Fq "attempting Latitude power_on for ash-earth before exit" "${tmpdir}/stderr.log"
printf 'ok\n' >"${out_file}"

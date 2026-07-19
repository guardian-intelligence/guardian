#!/usr/bin/env bash
# Hermetic guards on the golden-image build inputs: build.sh must parse,
# answer --help, and reject unknown flags without touching the network or
# needing root; pins.env must stay a pure key="value" file with every
# artifact version paired to a well-formed sha256. A malformed pin would
# otherwise surface only at build time on the tracer host.
set -euo pipefail

build_sh="${1:?usage: image_test.sh <build.sh> <pins.env>}"
pins_env="${2:?usage: image_test.sh <build.sh> <pins.env>}"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

bash -n "${build_sh}" || fail "build.sh does not parse"
grep -Fq "git build-essential pkg-config cryptsetup-bin sudo unzip" "${build_sh}" ||
  fail "build.sh does not install the runner bootstrap packages"
grep -Fq 'rootfs_size="80G"' "${build_sh}" ||
  fail "build.sh does not provision the 4-vCPU root-disk size"
grep -Fq 'rootfs_min_free_bytes=$((64 * 1024 * 1024 * 1024))' "${build_sh}" ||
  fail "build.sh does not enforce rootfs free-space headroom"
grep -Fq "runner ALL=(ALL:ALL) NOPASSWD: ALL" "${build_sh}" ||
  fail "build.sh does not grant the runner passwordless sudo"
grep -Fq 'chmod 0440 "${mnt}/etc/sudoers.d/runner"' "${build_sh}" ||
  fail "runner sudoers policy does not have the required mode"

help_out="$(bash "${build_sh}" --help 2>&1)" || fail "build.sh --help exited non-zero"
grep -q "usage:" <<<"${help_out}" || fail "build.sh --help printed no usage"

if bash "${build_sh}" --bogus-flag >/dev/null 2>&1; then
  fail "build.sh accepted an unknown flag"
fi

malformed="$(grep -vE '^(#.*)?$' "${pins_env}" | grep -vE '^[A-Z][A-Z0-9_]*="[^"$`\\]*"$' || true)"
if [[ -n "${malformed}" ]]; then
  fail "pins.env has non-assignment lines: ${malformed}"
fi

(
  set -euo pipefail
  # shellcheck source=pins.env
  source "${pins_env}"
  for var in UBUNTU_SERIAL UBUNTU_SHA256 RUNNER_VERSION RUNNER_SHA256 NODE_VERSION NODE_SHA256; do
    [[ -n "${!var:-}" ]] || fail "pins.env is missing ${var}"
  done
  for var in UBUNTU_SHA256 RUNNER_SHA256 NODE_SHA256; do
    [[ "${!var}" =~ ^[0-9a-f]{64}$ ]] || fail "${var} is not a lowercase sha256"
  done
  [[ "${UBUNTU_SERIAL}" =~ ^[0-9]{8}(\.[0-9]+)?$ ]] || fail "UBUNTU_SERIAL is not a release serial"
  [[ "${RUNNER_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "RUNNER_VERSION is not x.y.z"
  [[ "${NODE_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "NODE_VERSION is not x.y.z"
)

echo "golden-image inputs OK"

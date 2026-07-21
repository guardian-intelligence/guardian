#!/usr/bin/env bash
# Hermetic guards on the golden-image build inputs: build.sh must parse,
# answer --help, and reject unknown flags without touching the network or
# needing root; pins.env must stay a pure key="value" file with every
# artifact version paired to a well-formed sha256. A malformed pin would
# otherwise surface only at build time on the tracer host.
set -euo pipefail

build_sh="${1:?usage: image_test.sh <build.sh> <build-upstream.sh> <render-qemu-template.py> <pins.env>}"
build_upstream_sh="${2:?}"
renderer="${3:?}"
pins_env="${4:?}"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

bash -n "${build_sh}" || fail "build.sh does not parse"
bash -n "${build_upstream_sh}" || fail "build-upstream.sh does not parse"
python3 -c 'compile(open(__import__("sys").argv[1]).read(), __import__("sys").argv[1], "exec")' \
  "${renderer}" || fail "render-qemu-template.py does not parse"
grep -Fq '"${script_dir}/build-upstream.sh"' "${build_sh}" ||
  fail "build.sh does not consume the cached runner-images qcow2"
grep -Fq "cryptsetup-bin" "${build_sh}" ||
  fail "build.sh does not install the Postflight runtime package"
grep -Fq 'CRIU_SHA256=' "${pins_env}" ||
  fail "pins.env does not pin the CRIU archive"
grep -Fq 'TINI_VERSION=' "${pins_env}" ||
  fail "pins.env does not pin the native capsule init"
grep -Fq 'RUNNER_SOURCE_COMMIT=' "${pins_env}" ||
  fail "pins.env does not pin the patched runner source"
grep -Fq 'RUNNER_LISTENER_DLL' "${build_sh}" ||
  fail "build.sh does not require the patched runner listener"
grep -Fq 'Runner.Worker.real' "${build_sh}" ||
  fail "build.sh does not preserve the official worker behind the capsule wrapper"
grep -Fq 'guestd validate-assignment' "${build_sh}" ||
  fail "job-start hook does not perform assignment validation"
grep -Fq '"tini=${TINI_VERSION}"' "${build_sh}" ||
  fail "build.sh does not install the pinned native capsule init"
grep -Fq 'NETWORK_LOCK_DEFAULT=NETWORK_LOCK_SKIP' "${build_sh}" ||
  fail "build.sh does not build the pinned CRIU network policy"
grep -Fq 'rootfs_size="80G"' "${build_sh}" ||
  fail "build.sh does not provision the 4-vCPU root-disk size"
grep -Fq '[[ "${growpart_output}" != NOCHANGE:* ]]' "${build_sh}" ||
  fail "build.sh does not accept an already-expanded runner-images partition"
grep -Fq 'rootfs_min_free_bytes=$((17 * 1024 * 1024 * 1024))' "${build_sh}" ||
  fail "build.sh does not preserve runner-images free-space headroom"
grep -Fq "runner ALL=(ALL:ALL) NOPASSWD: ALL" "${build_sh}" ||
  fail "build.sh does not grant the runner passwordless sudo"
grep -Fq 'chmod 0440 "${mnt}/etc/sudoers.d/runner"' "${build_sh}" ||
  fail "runner sudoers policy does not have the required mode"
grep -Fq 'PACKER_TIMEOUT' "${build_upstream_sh}" ||
  fail "upstream image build has no hard timeout"
grep -Fq 'qemu-img check -q "${cached_image}"' "${build_upstream_sh}" ||
  fail "upstream image cache is not integrity checked"
grep -Fq 'cpu_model            = var.qemu_cpu_model' "${renderer}" ||
  fail "QEMU image builds do not expose the hardware CPU feature set"
grep -Fq 'ELF 64-bit LSB executable, x86-64,' "${build_sh}" ||
  fail "build.sh does not reject a non-executable or wrong-architecture guestd input"

help_out="$(bash "${build_sh}" --help 2>&1)" || fail "build.sh --help exited non-zero"
grep -q "usage:" <<<"${help_out}" || fail "build.sh --help printed no usage"

if bash "${build_sh}" --bogus-flag >/dev/null 2>&1; then
  fail "build.sh accepted an unknown flag"
fi

upstream_help="$(bash "${build_upstream_sh}" --help 2>&1)" ||
  fail "build-upstream.sh --help exited non-zero"
grep -q "usage:" <<<"${upstream_help}" ||
  fail "build-upstream.sh --help printed no usage"
if bash "${build_upstream_sh}" --bogus-flag >/dev/null 2>&1; then
  fail "build-upstream.sh accepted an unknown flag"
fi

fixture_dir="$(mktemp -d)"
trap 'rm -rf "${fixture_dir}"' EXIT
cat >"${fixture_dir}/upstream.pkr.hcl" <<'EOF'
build {
  sources = ["source.azure-arm.image"]
  name = "ubuntu-24_04"

  provisioner "shell" {
    inline = ["preserve-upstream-provisioner"]
  }

  provisioner "shell" {
    execute_command = "sudo sh -c '{{ .Vars }} {{ .Path }}'"
    inline          = ["sleep 30", "/usr/sbin/waagent -force -deprovision+user && export HISTSIZE=0 && sync"]
  }

}
EOF
"${renderer}" --plugin-version 1.2.3 \
  "${fixture_dir}/upstream.pkr.hcl" "${fixture_dir}/rendered.pkr.hcl"
grep -Fq 'sources = ["source.qemu.image"]' "${fixture_dir}/rendered.pkr.hcl" ||
  fail "renderer did not select QEMU"
grep -Fq 'version = "= 1.2.3"' "${fixture_dir}/rendered.pkr.hcl" ||
  fail "renderer did not pin the QEMU plugin"
grep -Fq "preserve-upstream-provisioner" "${fixture_dir}/rendered.pkr.hcl" ||
  fail "renderer dropped an upstream provisioner"
if grep -Fq "waagent -force -deprovision" "${fixture_dir}/rendered.pkr.hcl"; then
  fail "renderer retained Azure deprovisioning"
fi

malformed="$(grep -vE '^(#.*)?$' "${pins_env}" | grep -vE '^[A-Z][A-Z0-9_]*="[^"$`\\]*"$' || true)"
if [[ -n "${malformed}" ]]; then
  fail "pins.env has non-assignment lines: ${malformed}"
fi

(
  set -euo pipefail
  # shellcheck source=pins.env
  source "${pins_env}"
  for var in RUNNER_IMAGES_REF RUNNER_IMAGES_VERSION RUNNER_IMAGES_COMMIT UBUNTU_SERIAL UBUNTU_SHA256 \
    PACKER_VERSION PACKER_SHA256 PACKER_QEMU_PLUGIN_VERSION PACKER_QEMU_PLUGIN_SHA256 \
    RUNNER_VERSION RUNNER_SHA256 RUNNER_SOURCE_COMMIT RUNNER_SOURCE_SHA256 DOTNET_SDK_VERSION DOTNET_SDK_SHA512 \
    CRIU_VERSION CRIU_COMMIT CRIU_SHA256 TINI_VERSION; do
    [[ -n "${!var:-}" ]] || fail "pins.env is missing ${var}"
  done
  for var in UBUNTU_SHA256 PACKER_SHA256 PACKER_QEMU_PLUGIN_SHA256 RUNNER_SHA256 RUNNER_SOURCE_SHA256 CRIU_SHA256; do
    [[ "${!var}" =~ ^[0-9a-f]{64}$ ]] || fail "${var} is not a lowercase sha256"
  done
  [[ "${RUNNER_IMAGES_COMMIT}" =~ ^[0-9a-f]{40}$ ]] ||
    fail "RUNNER_IMAGES_COMMIT is not a full lowercase commit"
  [[ "${RUNNER_SOURCE_COMMIT}" =~ ^[0-9a-f]{40}$ ]] ||
    fail "RUNNER_SOURCE_COMMIT is not a full lowercase commit"
  [[ "${DOTNET_SDK_SHA512}" =~ ^[0-9a-f]{128}$ ]] ||
    fail "DOTNET_SDK_SHA512 is not a lowercase sha512"
  [[ "${CRIU_COMMIT}" =~ ^[0-9a-f]{40}$ ]] ||
    fail "CRIU_COMMIT is not a full lowercase commit"
  [[ "${RUNNER_IMAGES_REF}" =~ ^ubuntu24/[0-9]{8}\.[0-9]+$ ]] ||
    fail "RUNNER_IMAGES_REF is not an Ubuntu 24 release tag"
  [[ "${RUNNER_IMAGES_VERSION}" =~ ^[0-9]{8}\.[0-9]+\.[0-9]+$ ]] ||
    fail "RUNNER_IMAGES_VERSION is not an image release"
  [[ "${UBUNTU_SERIAL}" =~ ^[0-9]{8}(\.[0-9]+)?$ ]] || fail "UBUNTU_SERIAL is not a release serial"
  [[ "${PACKER_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "PACKER_VERSION is not x.y.z"
  [[ "${PACKER_QEMU_PLUGIN_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
    fail "PACKER_QEMU_PLUGIN_VERSION is not x.y.z"
  [[ "${RUNNER_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "RUNNER_VERSION is not x.y.z"
  [[ "${CRIU_VERSION}" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] || fail "CRIU_VERSION is not numeric"
  [[ "${TINI_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+-[0-9]+$ ]] || fail "TINI_VERSION is not an Ubuntu package version"
)

echo "golden-image inputs OK"

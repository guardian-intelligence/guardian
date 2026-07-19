#!/usr/bin/env bash
# Build and cache the pinned actions/runner-images Ubuntu userspace using its
# original provisioners and toolset, with a thin QEMU Packer source in place
# of Azure. The completed qcow2 path is the only stdout output.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  cat >&2 <<EOF
usage: build-upstream.sh [--help]

environment:
  WORK_DIR          cache + build root (default: /var/tmp/postflight-image)
  QEMU_ACCELERATOR  Packer QEMU accelerator (default: kvm)
  QEMU_BINARY       QEMU binary (default: /usr/bin/qemu-system-x86_64)
  QEMU_CPUS         image-build vCPUs (default: 4)
  QEMU_MEMORY_MIB   image-build memory MiB (default: 16384)
  PACKER_TIMEOUT    hard build timeout (default: 8h)
EOF
}

if [[ "$#" -gt 0 ]]; then
  case "$1" in
  -h | --help)
    usage
    exit 0
    ;;
  *)
    usage
    exit 1
    ;;
  esac
fi

log() {
  echo "$@" >&2
}

die() {
  echo "build-upstream.sh: $*" >&2
  exit 1
}

for cmd in curl genisoimage git qemu-img sha256sum ssh-keygen tee timeout unzip; do
  command -v "${cmd}" >/dev/null 2>&1 || die "missing required command: ${cmd}"
done

# shellcheck source=pins.env
source "${script_dir}/pins.env"
for var in RUNNER_IMAGES_REF RUNNER_IMAGES_VERSION RUNNER_IMAGES_COMMIT UBUNTU_SERIAL UBUNTU_SHA256 \
  PACKER_VERSION PACKER_SHA256 PACKER_QEMU_PLUGIN_VERSION PACKER_QEMU_PLUGIN_SHA256; do
  [[ -n "${!var:-}" ]] || die "pins.env is missing ${var}"
done

work_dir="${WORK_DIR:-/var/tmp/postflight-image}"
qemu_accelerator="${QEMU_ACCELERATOR:-kvm}"
qemu_binary="${QEMU_BINARY:-/usr/bin/qemu-system-x86_64}"
qemu_cpus="${QEMU_CPUS:-4}"
qemu_memory_mib="${QEMU_MEMORY_MIB:-16384}"
packer_timeout="${PACKER_TIMEOUT:-8h}"

[[ -x "${qemu_binary}" ]] || die "QEMU binary is not executable: ${qemu_binary}"
[[ "${qemu_cpus}" =~ ^[1-9][0-9]*$ ]] || die "QEMU_CPUS must be a positive integer"
[[ "${qemu_memory_mib}" =~ ^[1-9][0-9]*$ ]] || die "QEMU_MEMORY_MIB must be a positive integer"
if [[ "${qemu_accelerator}" == "kvm" && ! -r /dev/kvm ]]; then
  die "/dev/kvm is unavailable (set QEMU_ACCELERATOR=tcg only for template smoke tests)"
fi

mkdir -p "${work_dir}"

fetch() {
  local url="$1"
  local dest="$2"
  local sha256="$3"
  if [[ -f "${dest}" && "$(sha256sum "${dest}" | awk '{print $1}')" == "${sha256}" ]]; then
    log "cached: ${dest}"
    return 0
  fi
  log "fetching ${url}"
  curl -fsSL --retry 3 -o "${dest}.partial" "${url}"
  local actual
  actual="$(sha256sum "${dest}.partial" | awk '{print $1}')"
  if [[ "${actual}" != "${sha256}" ]]; then
    rm -f "${dest}.partial"
    die "checksum mismatch for ${url}: expected ${sha256}, got ${actual}"
  fi
  mv "${dest}.partial" "${dest}"
}

packer_zip="packer_${PACKER_VERSION}_linux_amd64.zip"
packer_url="https://releases.hashicorp.com/packer/${PACKER_VERSION}/${packer_zip}"
fetch "${packer_url}" "${work_dir}/${packer_zip}" "${PACKER_SHA256}"
packer_dir="${work_dir}/packer-${PACKER_VERSION}"
packer="${packer_dir}/packer"
if [[ ! -x "${packer}" ]]; then
  mkdir -p "${packer_dir}"
  unzip -q -o "${work_dir}/${packer_zip}" -d "${packer_dir}"
fi
[[ -x "${packer}" ]] || die "Packer extraction did not produce ${packer}"

source_dir="${work_dir}/runner-images-${RUNNER_IMAGES_COMMIT}"
if [[ ! -d "${source_dir}/.git" ]]; then
  mkdir -p "${source_dir}"
  git -C "${source_dir}" init -q
  git -C "${source_dir}" remote add origin https://github.com/actions/runner-images.git
fi
if ! git -C "${source_dir}" cat-file -e "${RUNNER_IMAGES_COMMIT}^{commit}" 2>/dev/null; then
  log "fetching actions/runner-images ${RUNNER_IMAGES_REF} (${RUNNER_IMAGES_COMMIT})"
  git -C "${source_dir}" fetch --depth=1 origin "${RUNNER_IMAGES_COMMIT}"
fi
git -C "${source_dir}" checkout -q --detach "${RUNNER_IMAGES_COMMIT}"
actual_commit="$(git -C "${source_dir}" rev-parse HEAD)"
[[ "${actual_commit}" == "${RUNNER_IMAGES_COMMIT}" ]] ||
  die "runner-images checkout is ${actual_commit}, expected ${RUNNER_IMAGES_COMMIT}"
tag_commit="$(
  git -C "${source_dir}" ls-remote --exit-code origin "refs/tags/${RUNNER_IMAGES_REF}" |
    awk 'NR == 1 { print $1 }'
)"
[[ "${tag_commit}" == "${RUNNER_IMAGES_COMMIT}" ]] ||
  die "runner-images tag ${RUNNER_IMAGES_REF} is ${tag_commit}, expected ${RUNNER_IMAGES_COMMIT}"

# The cache binds every input to the generated Packer template, not merely
# the upstream checkout. Otherwise a Canonical/Packer pin or adapter change
# could silently reuse bytes created by an older build recipe.
cache_input_sha256="$(
  {
    sha256sum "${script_dir}/pins.env" | awk '{print $1}'
    sha256sum "${script_dir}/render-qemu-template.py" | awk '{print $1}'
  } | sha256sum | awk '{print $1}'
)"
cache_key="${RUNNER_IMAGES_COMMIT}-${cache_input_sha256:0:16}"
cache_dir="${work_dir}/runner-images-qemu-${cache_key}"
cached_image="${cache_dir}/runner-images.qcow2"
if [[ -f "${cached_image}" ]] && qemu-img check -q "${cached_image}"; then
  log "cached runner image: ${cached_image}"
  echo "${cached_image}"
  exit 0
fi
[[ ! -e "${cache_dir}" ]] ||
  die "incomplete cache exists at ${cache_dir}; preserve it for diagnosis, then move it aside before retrying"

key_dir="${work_dir}/packer-key-${cache_key}"
mkdir -p "${key_dir}"
private_key="${key_dir}/id_ed25519"
if [[ ! -f "${private_key}" ]]; then
  ssh-keygen -q -t ed25519 -N "" -C postflight-image-builder -f "${private_key}"
fi
public_key="$(cat "${private_key}.pub")"

cloud_init_dir="${work_dir}/cloud-init-${cache_key}"
mkdir -p "${cloud_init_dir}"
printf 'instance-id: postflight-%s\nlocal-hostname: postflight-image-builder\n' \
  "${RUNNER_IMAGES_COMMIT}" >"${cloud_init_dir}/meta-data"
cat >"${cloud_init_dir}/user-data" <<EOF
#cloud-config
ssh_pwauth: false
disable_root: true
users:
  - name: packer
    uid: 1000
    groups: [adm, sudo]
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    lock_passwd: true
    ssh_authorized_keys:
      - ${public_key}
package_update: true
packages:
  - walinuxagent
growpart:
  mode: auto
  devices: ['/']
resize_rootfs: true
EOF

template_dir="${source_dir}/images/ubuntu/templates"
upstream_template="${template_dir}/build.ubuntu-24_04.pkr.hcl"
rendered_template="${template_dir}/postflight.${cache_key}.pkr.hcl"
"${script_dir}/render-qemu-template.py" \
  --plugin-version "${PACKER_QEMU_PLUGIN_VERSION}" \
  "${upstream_template}" "${rendered_template}"

ubuntu_image="ubuntu-24.04-server-cloudimg-amd64.img"
ubuntu_url="https://cloud-images.ubuntu.com/releases/noble/release-${UBUNTU_SERIAL}/${ubuntu_image}"
building_dir="${work_dir}/runner-images-qemu-${cache_key}.building.$$"
log_file="${building_dir}.log"

export PACKER_CACHE_DIR="${work_dir}/packer-cache"
export PACKER_PLUGIN_PATH="${work_dir}/packer-plugins"
mkdir -p "${PACKER_CACHE_DIR}" "${PACKER_PLUGIN_PATH}"

log "initializing Packer QEMU plugin ${PACKER_QEMU_PLUGIN_VERSION}"
"${packer}" init "${rendered_template}"
plugin="${PACKER_PLUGIN_PATH}/github.com/hashicorp/qemu/packer-plugin-qemu_v${PACKER_QEMU_PLUGIN_VERSION}_x5.0_linux_amd64"
[[ -x "${plugin}" ]] || die "Packer init did not produce ${plugin}"
actual_plugin_sha256="$(sha256sum "${plugin}" | awk '{print $1}')"
[[ "${actual_plugin_sha256}" == "${PACKER_QEMU_PLUGIN_SHA256}" ]] ||
  die "QEMU plugin checksum mismatch: expected ${PACKER_QEMU_PLUGIN_SHA256}, got ${actual_plugin_sha256}"
log "validating runner-images QEMU template"
"${packer}" validate \
  -var "cloud_init_meta_data=${cloud_init_dir}/meta-data" \
  -var "cloud_init_user_data=${cloud_init_dir}/user-data" \
  -var "image_version=${RUNNER_IMAGES_VERSION}" \
  -var "output_directory=${building_dir}" \
  -var "qemu_accelerator=${qemu_accelerator}" \
  -var "qemu_binary=${qemu_binary}" \
  -var "qemu_cpus=${qemu_cpus}" \
  -var "qemu_memory_mib=${qemu_memory_mib}" \
  -var "source_image_sha256=${UBUNTU_SHA256}" \
  -var "source_image_url=${ubuntu_url}" \
  -var "ssh_private_key_file=${private_key}" \
  "${rendered_template}"

log "building runner-images ${RUNNER_IMAGES_REF}; live log: ${log_file}"
if ! timeout --foreground "${packer_timeout}" \
  "${packer}" build -color=false -on-error=abort \
  -var "cloud_init_meta_data=${cloud_init_dir}/meta-data" \
  -var "cloud_init_user_data=${cloud_init_dir}/user-data" \
  -var "image_version=${RUNNER_IMAGES_VERSION}" \
  -var "output_directory=${building_dir}" \
  -var "qemu_accelerator=${qemu_accelerator}" \
  -var "qemu_binary=${qemu_binary}" \
  -var "qemu_cpus=${qemu_cpus}" \
  -var "qemu_memory_mib=${qemu_memory_mib}" \
  -var "source_image_sha256=${UBUNTU_SHA256}" \
  -var "source_image_url=${ubuntu_url}" \
  -var "ssh_private_key_file=${private_key}" \
  "${rendered_template}" 2>&1 | tee "${log_file}"; then
  die "Packer build failed or exceeded ${packer_timeout}; evidence retained at ${log_file}"
fi

built_image="${building_dir}/runner-images.qcow2"
[[ -f "${built_image}" ]] || die "Packer completed without ${built_image}"
qemu-img check -q "${built_image}" || die "Packer produced an invalid qcow2"
mv "${building_dir}" "${cache_dir}"
log "cached runner image: ${cached_image}"
echo "${cached_image}"

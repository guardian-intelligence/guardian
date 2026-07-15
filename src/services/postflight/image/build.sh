#!/usr/bin/env bash
# Build the Postflight golden runner image and template it into ZFS as
# <pool>/postflight/images/<image-id>@golden. The image carries everything a
# runner VM needs — Ubuntu 24.04, the pinned actions/runner tree, Node.js,
# git, guestd — and zero customer bytes; workload always arrives later, via
# the workspace zvol. hostd clones one root disk per slot from @golden and
# destroys it with the VM.
#
# Runs as root on a plain Ubuntu host with qemu-utils and zfsutils-linux:
#
#   sudo env POOL=tank GUESTD_BIN=/path/to/guestd \
#     src/services/postflight/image/build.sh
#
# Every artifact in pins.env is sha256-verified before use; a mismatch
# aborts the build. Re-runs are idempotent: the image id derives from the
# pins file, the guestd binary, and the repo commit, and an @golden snapshot
# that already exists is left untouched. See README.md for the full runbook.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  cat >&2 <<EOF
usage: build.sh [--help]

environment:
  GUESTD_BIN  linux/amd64 guestd binary to bake into the image (required)
  POOL        zpool that receives the image dataset (default: tank)
  WORK_DIR    download cache + scratch space (default: /var/tmp/postflight-image)
  NBD_DEVICE  nbd device to attach the image to (default: first free /dev/nbdN)
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
  echo "build.sh: $*" >&2
  exit 1
}

for cmd in blkid chroot curl git growpart modprobe python3 qemu-img qemu-nbd resize2fs sha256sum tar udevadm xz zfs; do
  command -v "${cmd}" >/dev/null 2>&1 || die "missing required command: ${cmd}"
done
[[ "${EUID}" -eq 0 ]] || die "must run as root (qemu-nbd, chroot, and zfs)"
[[ -n "${GUESTD_BIN:-}" ]] || die "set GUESTD_BIN to the guestd binary to bake in"
[[ -f "${GUESTD_BIN}" ]] || die "GUESTD_BIN not found: ${GUESTD_BIN}"

# shellcheck source=pins.env
source "${script_dir}/pins.env"
for var in UBUNTU_SERIAL UBUNTU_SHA256 RUNNER_VERSION RUNNER_SHA256 NODE_VERSION NODE_SHA256; do
  [[ -n "${!var:-}" ]] || die "pins.env is missing ${var}"
done

pool="${POOL:-tank}"
work_dir="${WORK_DIR:-/var/tmp/postflight-image}"

# Runner _diag logs, /tmp, and tool caches all land on the root disk at job
# time — only the job workspace lives on the workspace zvol — and nothing
# grows the rootfs at boot (cloud-init is purged), so the build grows it and
# asserts the margin actually materialized.
rootfs_size="8G"
rootfs_min_free_bytes=$((4 * 1024 * 1024 * 1024))

ubuntu_image="ubuntu-24.04-minimal-cloudimg-amd64.img"
ubuntu_url="https://cloud-images.ubuntu.com/minimal/releases/noble/release-${UBUNTU_SERIAL}/${ubuntu_image}"
runner_tarball="actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz"
runner_url="https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/${runner_tarball}"
node_tarball="node-v${NODE_VERSION}-linux-x64.tar.xz"
node_url="https://nodejs.org/dist/v${NODE_VERSION}/${node_tarball}"

pins_sha256="$(sha256sum "${script_dir}/pins.env" | awk '{print $1}')"
guestd_sha256="$(sha256sum "${GUESTD_BIN}" | awk '{print $1}')"
commit="$(git -C "${script_dir}" rev-parse HEAD)"
commit_short="${commit:0:12}"
diff_sha256=""
if ! git -C "${script_dir}" diff-index --quiet HEAD --; then
  diff_sha256="$(git -C "${script_dir}" diff HEAD | sha256sum | awk '{print $1}')"
  commit_short="${commit_short}-dirty"
  log "WARNING: building from a dirty tree; the image id records ${commit_short}"
fi
# The id binds every direct build input — pins, guestd binary, commit, and
# the working-tree diff when dirty — so the @golden idempotence
# short-circuit cannot serve different content under one id.
input_sha256="$(printf '%s\n' "${pins_sha256}" "${guestd_sha256}" "${commit}" "${diff_sha256}" |
  sha256sum | awk '{print $1}')"
image_id="noble-${input_sha256:0:12}-g${commit_short}"

dataset="${pool}/postflight/images/${image_id}"
scratch="${pool}/postflight/build/${image_id}"
if zfs list -H -o name "${dataset}@golden" >/dev/null 2>&1; then
  log "already templated: ${dataset}@golden"
  echo "${image_id}"
  exit 0
fi

mnt=""
mounted=false
resolv_moved=false
nbd=""
nbd_connected=false
scratch_created=false

cleanup() {
  set +e
  if [[ "${resolv_moved}" == true ]]; then
    mv -f "${mnt}/etc/resolv.conf.pristine" "${mnt}/etc/resolv.conf"
    resolv_moved=false
  fi
  if [[ "${mounted}" == true ]]; then
    rm -f "${mnt}/usr/sbin/policy-rc.d"
    umount -R "${mnt}" 2>/dev/null
    mounted=false
  fi
  if [[ "${nbd_connected}" == true ]]; then
    qemu-nbd --disconnect "${nbd}" >/dev/null 2>&1
    nbd_connected=false
  fi
  if [[ "${scratch_created}" == true ]]; then
    zfs destroy -r "${scratch}" 2>/dev/null
    scratch_created=false
  fi
}
trap cleanup EXIT

# fetch <url> <dest> <sha256>: cache hit only on an exact checksum match, so
# a truncated or tampered download can never be reused.
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

in_chroot() {
  chroot "${mnt}" /usr/bin/env \
    DEBIAN_FRONTEND=noninteractive \
    PATH=/usr/sbin:/usr/bin:/sbin:/bin \
    "$@"
}

mkdir -p "${work_dir}"
fetch "${ubuntu_url}" "${work_dir}/${ubuntu_image}" "${UBUNTU_SHA256}"
fetch "${runner_url}" "${work_dir}/${runner_tarball}" "${RUNNER_SHA256}"
fetch "${node_url}" "${work_dir}/${node_tarball}" "${NODE_SHA256}"

# Always start from the pristine download: a crashed run leaves a
# half-modified working copy behind, and re-entering it would compound edits.
work_image="${work_dir}/${image_id}.qcow2"
rm -f "${work_image}"
cp --reflink=auto "${work_dir}/${ubuntu_image}" "${work_image}"
qemu-img resize -q "${work_image}" "${rootfs_size}"

modprobe nbd max_part=16
# modprobe is a no-op when nbd is already loaded, and with max_part=0 the
# kernel never surfaces the image's partitions.
if [[ "$(cat /sys/module/nbd/parameters/max_part)" -eq 0 ]]; then
  die "nbd module loaded with max_part=0; rmmod nbd and re-run"
fi
nbd="${NBD_DEVICE:-}"
if [[ -z "${nbd}" ]]; then
  for candidate in /dev/nbd{0..15}; do
    if [[ -b "${candidate}" && ! -e "/sys/block/${candidate#/dev/}/pid" ]]; then
      nbd="${candidate}"
      break
    fi
  done
fi
[[ -n "${nbd}" ]] || die "no free nbd device (set NBD_DEVICE to override)"

qemu-nbd --connect "${nbd}" --format qcow2 "${work_image}"
nbd_connected=true
udevadm settle

# qemu-nbd returns before the kernel finishes scanning the partition table,
# and settle cannot wait for uevents that have not been queued yet, so the
# pN device nodes can still be absent on the first look.
for _ in $(seq 1 50); do
  [[ -b "${nbd}p1" ]] && break
  sleep 0.2
  udevadm settle
done
[[ -b "${nbd}p1" ]] || die "nbd partitions never appeared on ${nbd}"

root_part=""
boot_part=""
esp_part=""
for part in "${nbd}"p*; do
  [[ -b "${part}" ]] || continue
  case "$(blkid -s LABEL -o value "${part}" 2>/dev/null)" in
  cloudimg-rootfs) root_part="${part}" ;;
  BOOT) boot_part="${part}" ;;
  UEFI) esp_part="${part}" ;;
  esac
done
[[ -n "${root_part}" ]] || die "no cloudimg-rootfs partition on ${nbd}; image layout changed?"

growpart "${nbd}" "${root_part#"${nbd}"p}" >/dev/null
udevadm settle

mnt="$(mktemp -d "${work_dir}/mnt.XXXXXX")"
mount "${root_part}" "${mnt}"
mounted=true
resize2fs "${root_part}" >/dev/null
if [[ -n "${boot_part}" ]]; then
  mount "${boot_part}" "${mnt}/boot"
fi
if [[ -n "${esp_part}" ]]; then
  mount "${esp_part}" "${mnt}/boot/efi"
fi
mount -t proc proc "${mnt}/proc"
mount -t sysfs sys "${mnt}/sys"
mount --bind /dev "${mnt}/dev"
mount --bind /dev/pts "${mnt}/dev/pts"

# The pristine image's resolv.conf is a dangling symlink into /run; apt in
# the chroot needs the host's resolver for the duration of the build.
mv "${mnt}/etc/resolv.conf" "${mnt}/etc/resolv.conf.pristine"
resolv_moved=true
cp /etc/resolv.conf "${mnt}/etc/resolv.conf"

# Keep dpkg maintainer scripts from starting services inside the chroot.
printf '#!/bin/sh\nexit 101\n' >"${mnt}/usr/sbin/policy-rc.d"
chmod 0755 "${mnt}/usr/sbin/policy-rc.d"

log "installing packages"
in_chroot apt-get -q update
in_chroot apt-get -q -y --no-install-recommends install git

log "installing actions/runner ${RUNNER_VERSION}"
in_chroot useradd --uid 1001 --user-group --create-home --shell /bin/bash runner
install -d -m 0755 "${mnt}/opt/actions-runner"
tar -xzf "${work_dir}/${runner_tarball}" -C "${mnt}/opt/actions-runner"
# The marker file makes the runner refuse to self-update: image releases
# follow pins.env, on GitHub's retirement cadence, never in-place.
touch "${mnt}/opt/actions-runner/.disableupdate"
in_chroot bash /opt/actions-runner/bin/installdependencies.sh
in_chroot chown -R runner:runner /opt/actions-runner

log "installing node ${NODE_VERSION}"
# Upstream tarball entries are owned by the build user's uid 1001 — the same
# uid as the runner user, the identity that executes untrusted job code.
# Without --no-same-owner, root's tar preserves that and hands the runner
# write access to /usr/local/bin, the first entry on root's PATH.
tar -xJf "${work_dir}/${node_tarball}" -C "${mnt}/usr/local" --strip-components=1 --no-same-owner \
  "node-v${NODE_VERSION}-linux-x64/bin" \
  "node-v${NODE_VERSION}-linux-x64/include" \
  "node-v${NODE_VERSION}-linux-x64/lib" \
  "node-v${NODE_VERSION}-linux-x64/share"

log "installing guestd (sha256 ${guestd_sha256})"
install -m 0755 "${GUESTD_BIN}" "${mnt}/usr/local/bin/guestd"
cat >"${mnt}/etc/systemd/system/guestd.service" <<'EOF'
[Unit]
Description=Postflight guest agent

[Service]
ExecStart=/usr/local/bin/guestd
Restart=no

[Install]
WantedBy=multi-user.target
EOF
install -d -m 0755 "${mnt}/etc/systemd/system/multi-user.target.wants"
ln -sf /etc/systemd/system/guestd.service \
  "${mnt}/etc/systemd/system/multi-user.target.wants/guestd.service"

log "removing cloud-init and ssh"
in_chroot apt-get -q -y purge cloud-init openssh-server
in_chroot apt-get -q -y --purge autoremove
rm -rf "${mnt}/etc/cloud" "${mnt}/var/lib/cloud" "${mnt}/etc/ssh"

# Configuration never arrives over network or metadata, but the runner still
# needs egress to GitHub, so any NIC the host attaches just DHCPs.
install -d -m 0755 "${mnt}/etc/netplan"
cat >"${mnt}/etc/netplan/10-postflight.yaml" <<'EOF'
network:
  version: 2
  ethernets:
    all:
      match:
        name: "en*"
      dhcp4: true
EOF
chmod 0600 "${mnt}/etc/netplan/10-postflight.yaml"

in_chroot apt-get -q clean
rm -rf "${mnt}/var/lib/apt/lists/"*

rootfs_free_bytes="$(df --output=avail -B1 "${mnt}" | tail -n 1 | tr -d '[:space:]')"
[[ "${rootfs_free_bytes}" -ge "${rootfs_min_free_bytes}" ]] ||
  die "rootfs has ${rootfs_free_bytes} bytes free, need ${rootfs_min_free_bytes}; grow rootfs_size"
log "rootfs headroom: ${rootfs_free_bytes} bytes free"

: >"${mnt}/etc/machine-id"
printf '%s\n' "${image_id}" >"${mnt}/etc/postflight-image-release"

rm -f "${mnt}/usr/sbin/policy-rc.d"
mv -f "${mnt}/etc/resolv.conf.pristine" "${mnt}/etc/resolv.conf"
resolv_moved=false
umount -R "${mnt}"
mounted=false
rmdir "${mnt}"
qemu-nbd --disconnect "${nbd}" >/dev/null
nbd_connected=false
udevadm settle

log "templating ${dataset}@golden"
virtual_bytes="$(qemu-img info --output=json -f qcow2 "${work_image}" |
  python3 -c 'import json, sys; print(json.load(sys.stdin)["virtual-size"])')"
volsize=$(((virtual_bytes + 1048575) / 1048576 * 1048576))

zfs destroy -r "${scratch}" 2>/dev/null || true
zfs create -p -s -V "${volsize}" -o volmode=dev "${scratch}"
scratch_created=true
scratch_device="/dev/zvol/${scratch}"
for _ in $(seq 1 150); do
  [[ -e "${scratch_device}" ]] && break
  sleep 0.1
done
[[ -e "${scratch_device}" ]] || die "device ${scratch_device} never appeared"
qemu-img convert -f qcow2 -O raw -n "${work_image}" "${scratch_device}"
zfs snapshot "${scratch}@golden"

# send | recv instead of renaming the scratch zvol: recv is atomic, so a
# dataset under images/ either exists complete with its @golden snapshot or
# not at all — hostd can never clone a half-written template.
zfs create -p "${pool}/postflight/images"
zfs send "${scratch}@golden" | zfs recv -o volmode=dev "${dataset}"
zfs destroy -r "${scratch}"
scratch_created=false
rm -f "${work_image}"

log "templated ${dataset}@golden"
echo "${image_id}"

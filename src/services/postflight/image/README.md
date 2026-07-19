# Postflight golden runner image

One root disk image containing everything a runner VM needs and zero
customer bytes: Ubuntu 24.04, the pinned `actions/runner` tree, Node.js,
git, archive extraction, passwordless `sudo` for the single-job runner user,
and guestd. Workload always arrives later, via the workspace zvol.
Explicitly absent: docker, cloud-init, ssh (no ingress path into a runner
VM at all), k8s anything.

`build.sh` templates the image into ZFS as
`<pool>/postflight/images/<image-id>@golden`. hostd clones one root disk
per slot from `@golden` and destroys it with the VM. The image id derives
from `pins.env`, the guestd binary, and the repo commit, is baked into the
guest at `/etc/postflight-image-release`, and is the `platform_image_id`
dimension of the workspace scope key — any pin bump or guestd change mints
a new image identity.

## Build (on the tracer host)

Prerequisites: root, `qemu-utils`, `zfsutils-linux`, `cloud-guest-utils`
(growpart), `curl`, `git`, `python3`, `xz-utils`, the target zpool
imported, and network egress to cloud-images.ubuntu.com, github.com, and
nodejs.org.

guestd is not in the repo yet, so the build cannot run end-to-end until it
lands (`build.sh` fails closed without `GUESTD_BIN`). The intended
invocation once `//src/services/postflight/guestd` exists:

```sh
eval "$(scripts/bootstrap.sh path)"
bazel build //src/services/postflight/guestd
sudo env POOL=tank \
  GUESTD_BIN="$(bazel cquery --output=files //src/services/postflight/guestd)" \
  src/services/postflight/image/build.sh
```

The script prints the image id on stdout and logs everything else to
stderr. Every artifact is fetched into `WORK_DIR` (default
`/var/tmp/postflight-image`) and sha256-verified against `pins.env` before
use; any mismatch aborts. The rootfs is grown to 80GiB during the build
(nothing grows it at boot) and the script fails unless at least 64GiB
remains free after installs. This ephemeral root disk is separate from the
80GiB durable workspace zvol configured for the 4-vCPU runner class.
Re-runs are idempotent: downloads are cached by checksum, modification
always restarts from the pristine cloud image, the final dataset appears
atomically (`zfs send | zfs recv`), and an `@golden` snapshot that already
exists is left untouched.

## Verify

Boot smoke via the driver's conformance suite — clone from `@golden`, boot
under the merged QEMU driver, wait for a login prompt on the serial
console, hot-attach a workspace clone, and destroy:

```sh
sudo env \
  HOSTD_QEMU_TEST_ROOT=tank/postflight/conformance \
  HOSTD_QEMU_TEST_IMAGE=tank/postflight/images/<image-id>@golden \
  HOSTD_QEMU_TEST_QEMU=/usr/bin/qemu-system-x86_64 \
  bazel test //src/services/postflight/hostd/vm:vm_test --test_output=streamed
```

The suite injects a scripted guest seam, so it proves the image boots to
userspace but exercises nothing guest-side. The full smoke — guestd's
`hello` over vsock within the probe deadline and the runner reporting its
version — becomes an additional conformance case once guestd and the vsock
transport exist; until then the spot-check below is the only guestd
coverage, and it is static.

Spot-check the contents without booting (`volmode=full`, unlike the `dev`
that templates and per-slot clones use, surfaces the partition device
nodes):

```sh
sudo zfs clone -o volmode=full tank/postflight/images/<image-id>@golden tank/postflight/verify
sudo udevadm settle
sudo mount /dev/zvol/tank/postflight/verify-part1 /mnt
cat /mnt/etc/postflight-image-release          # the image id build.sh printed
cat /mnt/opt/actions-runner/.disableupdate     # exists, empty
test -x /mnt/usr/local/bin/guestd
test -L /mnt/etc/systemd/system/multi-user.target.wants/guestd.service
/mnt/usr/local/bin/node --version
test ! -e /mnt/etc/ssh && test ! -e /mnt/etc/cloud && echo "no ssh, no cloud-init"
sudo umount /mnt && sudo zfs destroy tank/postflight/verify
```

## Template

hostd resolves root disks through its per-class config: `vm.ClassConfig`'s
`Image` field takes the full snapshot name, so point the class at
`<pool>/postflight/images/<image-id>@golden`. Roll slots by letting
destroy-and-refill drain the old image — no VM is ever reused across jobs,
so the fleet converges within one job cycle.

## Re-release cadence

GitHub enforces a minimum runner version and retires old ones on ~30-day
windows, so the image is re-released on GitHub's cadence, not ours: a stale
image is a liveness failure (GitHub silently stops assigning jobs).
Renovate proposes the `RUNNER_VERSION` and `NODE_VERSION` bumps in
`pins.env`; the adjacent sha256 pins are completed by hand from the release
notes checksum table (`actions/runner`) and `SHASUMS256.txt` (Node.js). The
Ubuntu serial is bumped by hand from
`cloud-images.ubuntu.com/minimal/releases/noble/` alongside its
`SHA256SUMS` entry. Rebuilding and re-templating is one script run.

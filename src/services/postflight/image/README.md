# Postflight golden runner image

One root disk image containing everything a runner VM needs and zero
customer bytes: Ubuntu 24.04, the pinned `actions/runner` tree, Node.js,
git, and guestd. Workload always arrives later, via the workspace zvol.
Explicitly absent: docker, cloud-init, ssh (no ingress path into a runner
VM at all), k8s anything.

`build.sh` templates the image into ZFS as
`<pool>/postflight/images/<image-id>@golden`. hostd clones one root disk
per slot from `@golden` and destroys it with the VM. The image id derives
from `pins.env` and the repo commit, is baked into the guest at
`/etc/postflight-image-release`, and is the `platform_image_id` dimension
of the workspace scope key — any pin bump mints a new image identity.

## Build (on the tracer host)

Prerequisites: root, `qemu-utils`, `zfsutils-linux`, `curl`, `git`,
`python3`, `xz-utils`, the target zpool imported, and network egress to
cloud-images.ubuntu.com, github.com, and nodejs.org.

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
use; any mismatch aborts. Re-runs are idempotent: downloads are cached by
checksum, modification always restarts from the pristine cloud image, the
final dataset appears atomically (`zfs send | zfs recv`), and an `@golden`
snapshot that already exists is left untouched.

## Verify

Boot smoke via the driver's conformance suite — clone from `@golden`, boot
under the merged QEMU driver, and destroy:

```sh
sudo env \
  HOSTD_QEMU_TEST_ROOT=tank/postflight/conformance \
  HOSTD_QEMU_TEST_IMAGE=tank/postflight/images/<image-id>@golden \
  HOSTD_QEMU_TEST_QEMU=/usr/bin/qemu-system-x86_64 \
  bazel test //src/services/postflight/hostd/vm:vm_test --test_output=streamed
```

Spot-check the contents without booting:

```sh
sudo zfs clone -o volmode=dev tank/postflight/images/<image-id>@golden tank/postflight/verify
sudo mount /dev/zvol/tank/postflight/verify-part1 /mnt
cat /mnt/etc/postflight-image-release          # the image id build.sh printed
cat /mnt/opt/actions-runner/.disableupdate     # exists, empty
/mnt/usr/local/bin/node --version
test ! -e /mnt/etc/ssh && test ! -e /mnt/etc/cloud && echo "no ssh, no cloud-init"
sudo umount /mnt && sudo zfs destroy tank/postflight/verify
```

## Template

Point hostd at the new snapshot by setting `HOSTD_IMAGE_ID=<image-id>` in
its environment; the class config resolves root disks to
`<HOSTD_POOL>/images/<image-id>@golden`. Roll slots by letting
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

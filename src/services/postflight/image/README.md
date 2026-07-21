# Postflight golden runner image

One root disk image containing everything a runner VM needs and zero
customer bytes. `build-upstream.sh` checks out a pinned
`actions/runner-images` Ubuntu 24.04 release and runs its original Packer
provisioners and toolset in their original order, changing only the
Azure machine builder and Azure-agent deprovisioner. The result is cached
as a QEMU qcow2 by upstream commit. `build.sh` layers the pinned
`actions/runner`, the pinned CRIU release, cryptsetup, the single-job `runner` user, and guestd onto
that image, removes the temporary image-build ingress, and imports it into
ZFS. Workload always arrives later via the workspace zvol.

The customer-facing toolchain, Docker, and `/opt/hostedtoolcache` therefore
come from the same source release as GitHub-hosted runners. Explicitly
absent from the final image: cloud-init and ssh.

`build.sh` templates the image into ZFS as
`<pool>/postflight/images/<image-id>@golden`. hostd clones one root disk
per slot from `@golden` and destroys it with the VM. The image id derives
from `pins.env`, the guestd binary, and the repo commit, is baked into the
guest at `/etc/postflight-image-release`, and is the `platform_image_id`
dimension of the workspace scope key — any pin bump or guestd change mints
a new image identity.

## Build (on the tracer host)

Prerequisites: root, KVM, QEMU, `qemu-utils`, `zfsutils-linux`,
`cloud-guest-utils` (growpart), `genisoimage`, `curl`, `git`, `python3`,
`unzip`, the target zpool imported, and network egress to
cloud-images.ubuntu.com, github.com, HashiCorp releases, and every source
used by the upstream runner-images provisioners.

Build guestd and the source-pinned Runner.Listener patch, then pass both
paths explicitly:

```sh
eval "$(scripts/bootstrap.sh path)"
bazel build //src/services/postflight/guestd/cmd/guestd
listener="$(src/services/postflight/runner/build.sh)"
sudo env POOL=tank \
  GUESTD_BIN="$(bazel cquery --output=files //src/services/postflight/guestd/cmd/guestd)" \
  RUNNER_LISTENER_DLL="${listener}" \
  src/services/postflight/image/build.sh
```

The script prints the image id on stdout and logs everything else to
stderr. Every direct artifact is fetched into `WORK_DIR` (default
`/var/tmp/postflight-image`) and sha256-verified against `pins.env`; the
upstream repository is fetched by full commit. The expensive upstream
qcow2 cache binds the full pin set and QEMU adapter, and is checked with
`qemu-img` before reuse. The release tag is also required to resolve to the
pinned commit.
The Packer build has an eight-hour hard timeout by default and retains its
log and any incomplete output Packer leaves for diagnosis on failure. The
rootfs is 80GiB, matching Blacksmith's 4-vCPU runner, and must retain the
17GiB free-space floor enforced by runner-images itself. This ephemeral
root disk is separate from the 80GiB durable workspace zvol.

KVM image builds expose the host CPU model. QEMU's default `qemu64` omits
SSSE3, which is below the instruction-set baseline required by the upstream
Homebrew install. TCG template smoke tests use the emulated `max` CPU model.

Re-runs are idempotent: modification always restarts from the cached,
pristine upstream image, the final dataset appears atomically
(`zfs send | zfs recv`), and an `@golden` snapshot that already exists is
left untouched. Failed upstream build directories are deliberately not
deleted during this tracer phase.

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
userspace but does not exercise guestd. Until the conformance suite speaks
the real vsock protocol, the spot-check below is the static guestd coverage.

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
test "$(stat -c '%U:%G %a' /mnt/opt/actions-runner/bin/Runner.Worker)" = "root:root 4755"
test -L /mnt/etc/systemd/system/multi-user.target.wants/guestd.service
/mnt/usr/local/bin/node --version
test ! -e /mnt/usr/sbin/sshd && test ! -e /mnt/etc/cloud && echo "no ssh server, no cloud-init"
test -x /mnt/usr/bin/docker
test -d /mnt/opt/hostedtoolcache
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
windows, so the image is re-released on GitHub's cadence, not ours. Bump the
runner-images tag, image version, and full commit together, then bump the
Canonical server image serial/checksum to the release underlying that
image. Renovate proposes the `RUNNER_VERSION` bump; its adjacent sha256 is
completed from the release checksum table. Rebuilding and re-templating is
one script run. Setting `HOSTD_IMAGE_ID` to the printed ID makes the pool
governor destroy idle slots on older images and refill them from the new
`@golden`; active jobs drain normally.

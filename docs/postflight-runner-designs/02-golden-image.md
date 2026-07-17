# 02 — Golden image v0

> **EPHEMERAL.** Delete with this directory when the implementation pass completes.

The attested-substrate-to-be: one root disk image containing everything a
runner VM needs and **zero customer bytes**. Workload always arrives later,
via the workspace zvol. This is the functional v0; the full release lane
(reproducible mkosi build, SBOM, SLSA provenance, countersignature per
ADR 0010) is a fast-follow that must land before any external customer runs
on the system.

## Contents

| Piece | Pin | Why |
| - | - | - |
| Ubuntu 24.04 (cloud image, minimal) | exact image serial | base userland |
| `actions/runner` | exact version, sha256 of tarball | the job executor; `disableupdate` — we own updates because GitHub retires old versions on ~30-day windows, so image releases have an external cadence floor |
| guestd (01) | built from the repo at image-build commit | the only host-facing process |
| Node.js LTS | exact version | demo workload + the checkout action runtime |
| git | distro pin | checkout action |
| `runner` user, `/opt/actions-runner`, guestd systemd unit | — | layout |

Explicitly absent: docker (demo workflows are container-free), cloud-init
(disabled — configuration arrives over vsock, never over network or
metadata), ssh (no ingress path into a runner VM at all), k8s anything.

## Build (`src/services/postflight/image/`)

A single `build.sh` driven by a pins file (`pins.env`), runnable on the
tracer host:

1. Fetch the pinned cloud image, verify sha256.
2. Mount via qemu-nbd; install pinned artifacts into the tree; write systemd
   units; disable cloud-init/ssh; set the machine-id empty (per-boot).
3. Record `/etc/postflight-image-release` — image id = hash of the pins
   file + repo commit. This string is the `platform_image_id` dimension in
   the workspace scope key (04).
4. Unmount, convert to raw, `zfs recv` into `tank/postflight/images/<id>`
   and snapshot `@golden`. VM root disks are clones of `@golden` — one clone
   per slot, destroyed with the VM.

Determinism in v0 is best-effort (record every version, verify every
sha256); byte-reproducibility is the ADR-0010 lane's job. What must be exact
*now* is the QEMU argv (merged driver, `pc-q35-8.2` pin) and the pins file.

In the SNP phase the mutable disk image is not treated as measured
merely because QEMU launched it. QEMU uses stateless OVMF and direct
kernel/initramfs boot with `kernel-hashes=on`; the measured command line carries
the dm-verity root hash for this image. The root mounts read-only, persistent
NVRAM is absent, and writable runtime state lives in tmpfs or the separately
authenticated workspace.

## Runner version policy

GitHub enforces a minimum runner version and retires old ones. The image is
therefore re-released on GitHub's cadence, not ours: a Renovate rule watches
`actions/runner` releases and opens the pins-file PR; rebuilding and
re-templating is one script run. A stale image is a *liveness* failure
(GitHub silently stops assigning jobs), which the hammer's periodic runs
will surface before customers do.

## Testing

- Build script asserts every pin's checksum and fails closed.
- Smoke test on the tracer host: clone `@golden`, boot via the merged
  driver, `hello` over vsock within the probe deadline, runner binary
  reports its version, VM destroyed. This becomes a conformance case
  alongside the driver's existing 4.

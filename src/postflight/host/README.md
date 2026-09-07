# First-party Turbo host

`hosts/rust-forge-01.json` is the desired host configuration. The host serves
`postflight-4vcpu-ubuntu24-turbo`: two isolated 4-vCPU, 16-GiB KVM guests,
native encrypted ZFS, and no SEV-SNP requirement. The manifest pins the
installed QEMU version exactly. OS prerequisites must already be provisioned;
this reconciler never installs an unpinned system package.

An operator seeds these root-owned mode-0600 files outside Git:

- `/etc/postflight/secrets.env`: `HOSTD_SYNC_SECRET`, sourced by systemd.
- `/etc/postflight/zfs.key`: exactly 32 random bytes, held in OpenBao custody.
- `/etc/postflight/host.key`: 64 random bytes generated locally on first install.

Run installation commands from the reviewed, root-owned checkout. To
initialize only storage before building the golden image:

```sh
sudo python3 src/postflight/host/host.py storage \
  --manifest src/postflight/host/hosts/rust-forge-01.json
```

Storage creates a new 384-GiB file-backed `postflight_ci` pool only when the
declared backing file is absent. Existing files are import-only: a failed
import stops reconciliation. It never truncates, reformats, or overwrites an
existing vdev. The child `postflight_ci/postflight` uses AES-256-GCM with the
external raw key and mounts at `/var/lib/postflight` with mode 0751.

The initial installer accepts already-built artifacts from the reviewed
source. Build guestd and the patched listener with their existing Bazel and
runner scripts, then build the golden image with `IMAGE_FLAVOR=turbo`,
`POOL=postflight_ci`, and `WORK_DIR=/var/tmp/postflight-ci-image`. Install it:

```sh
sudo python3 src/postflight/host/host.py install \
  --manifest src/postflight/host/hosts/rust-forge-01.json \
  --hostd /absolute/path/to/hostd --image-id noble-turbo-IMAGE-gCOMMIT
```

Once this source is merged to protected `origin/main`, run the committed
`reconcile.sh rust-forge-01` as root or add `--enable-reconcile` to the
installer. The systemd timer follows the fixed public Guardian origin every
five minutes. It clones into root-owned `/opt/postflight/source`, checks that
main descends from the last applied commit, and hashes only runtime, host,
image, dependency, and build-tool inputs. Unrelated commits reuse the existing
hostd and golden image. Relevant changes build in root-owned locations, then
request a drain before changing installed runtime files or restarting any
runtime service. The durable request and acknowledgement live under
`/var/lib/postflight/maintenance`. The acknowledgement binds the exact
installation inputs to the host boot ID, live daemon's PID, and process start time. The
installer also requires zero active VM scopes before stopping hostd.
The interactive `/home/ubuntu` checkout is never an ongoing privileged input.

During a drain, hostd stops advertising capacity, preparing new listeners,
and refilling VMs. Never-registered VMs are retired; existing listeners may
receive their final job, which completes and seals normally. There is no
forced cancellation or drain deadline. An idle registered listener can keep
the installation pending until its single use completes: provider-side
assignment-safe listener retirement is not implemented. The installer returns
75 while pending; the reconciler treats this as a successful deferral, leaves
the applied receipts unchanged, and retries on the next timer.

A hostd version predating this drain protocol cannot acknowledge a request.
Its initial upgrade requires an operator-controlled maintenance window with
hostd stopped and no remaining VM scopes. The installer fails closed while
that older daemon is active; a stale acknowledgement or an idle-state poll
cannot authorize a restart. Pending requests survive installer failure and
reboot, and admission reopens only after all new runtime inputs are installed.

The storage unit imports and unlocks ZFS before hostd at boot. The network
unit installs `pfbr0`, per-host DHCP/DNS, public egress NAT, private/reserved
IPv4 egress denial, IPv6 denial, and guest-to-guest bridge denial. Guests can
reach the host only for DHCP, DNS, and checkout on port 8480. Firewall updates
replace only the `inet postflight_ci` and `bridge postflight_ci` tables in one
transaction. The manifest declares the existing UFW host firewall. After
the nft deny boundary is installed, the network unit adds only commented,
bridge-scoped UFW rules for DHCP (including source `0.0.0.0` before a lease),
gateway DNS, checkout, and IPv4 forwarding. UFW deduplicates and persists
these rules; its defaults and unrelated rules remain intact. The provisioner
refuses an inactive UFW or a different bridge/subnet identity instead of
enabling a firewall or leaving stale allows on a renamed bridge.

This ordering matters: an nft `accept` is not final across base chains,
whereas a `drop` cannot be overridden by a later chain. The Postflight hook
at priority -20 drops private/reserved destinations, spoofed sources, IPv6,
guest-to-guest traffic, and unsolicited ingress before UFW's priority-0
forwarding allows. Only established/related return traffic survives the
Postflight ingress filter. See the [nftables verdict contract](https://netfilter.org/projects/nftables/manpage.html)
and [UFW rule documentation](https://manpages.ubuntu.com/manpages/noble/man8/ufw.8.html).

`host.py render --manifest ... --image-id ... --output /tmp/postflight-render`
produces reviewable systemd, environment, dnsmasq, and nftables files without
changing the host. `bazelisk test //src/postflight/host:host_test` checks the
network and storage refusal contracts without root or a live host.

## Adopting the one-time upstream build

An in-flight build may have started in an `ubuntu`-owned
`/var/tmp/postflight-ci-image`. The reconciler intentionally refuses that
directory. Do not enable the timer until the build has exited and the cache
handoff below is complete. Renaming or changing ownership while Packer, QEMU,
qemu-nbd, or a chroot mount still uses it is not a safe handoff.

1. Record the completed upstream build's source commit, full pins, rendered
   adapter digest, QEMU version, log/result, exact recipe cache key, and
   SHA-256 of its `runner-images.qcow2`. Compare the pins/adapter with merged
   source. A digest identifies bytes; it does not establish trusted build
   provenance. If the source or build cannot be accounted for, rebuild.
2. After every build process exits and its mounts/NBD attachments are gone,
   preserve the old directory under a distinct evidence path. Create a new
   root-owned mode-0700 `/var/tmp/postflight-ci-image`. Do not recursively
   chown the old tree into the privileged build path.
3. Copy only the completed, standalone `runner-images.qcow2` into a new
   root-owned staging directory. Copy bytes into a new file (no hard links),
   reject symlink/nonregular input, compare the copied SHA-256 with the
   recorded value, and retain mode 0600. With the pinned system `qemu-img`,
   require `info -f qcow2 --output=json` to show no backing file or external
   data file and require `check -f qcow2` to succeed. Publish it atomically as
   `runner-images-qemu-<exact-current-cache-key>/runner-images.qcow2` in the
   new cache. Never overwrite an existing destination or reuse `.building`
   output. Preserve the receipt outside Git beside the artifact.
4. Copy no extracted Packer/plugin/SDK executables, patched runner source,
   Git metadata, temporary SSH keys, mount trees, or final guest images from
   the mutable cache. A fresh root cache downloads pinned tools and fetches
   pinned upstream source itself. The operator's reviewed one-time upstream
   image is the only adopted artifact.
5. Run the merged reconciler. It rebuilds hostd, guestd, the patched Listener,
   and the final Turbo image layer from root-owned source, while reusing the
   accounted-for upstream base. Do not copy an old `image-id` or
   `applied-inputs` receipt to bypass that build.

The cache key comes from `image/build-upstream.sh`'s full pins plus rendered
QEMU adapter digest. A similarly named directory or `qemu-img check` success
alone is not provenance. Subsequent root-owned caches are normal reconciler
state; customer guests cannot write them.

## First-install review

The installer checks its exact QEMU version, firmware, secret-file ownership,
existing pool identity, native encryption contract, and golden snapshot
before starting hostd. It validates dnsmasq syntax, the nft transaction, and
the declared active UFW firewall. Other host rules can still reject traffic.
Verify DHCP, public
egress, private/IPv6/guest-to-guest denial, host service exclusions, and the
checkout path on the live host. Then verify `postflight-storage`,
`postflight-network`, `postflight-dnsmasq`, and `hostd` are healthy, and inspect
the authenticated operator status without printing its bearer.

A process being active does not prove successful warm restore. Require the
host template-ready event, two simultaneous guest identities and leases,
actual GitHub jobs and streaming logs, and the cross-VM workspace/tool
lineage in [the CI proof contract](../../../docs/postflight-ci-migration.md).
A later boot test must demonstrate encrypted import/unlock and hostd recovery;
unit tests do not establish boot ordering on this host.

# WUM worker reinstall

This procedure wipes and rebuilds a disposable regional WUM worker in the
`guardian-mgmt` Kubernetes cluster. Unlike the management control planes, this
worker uses ordinary Talos with firmware Secure Boot disabled. It has no etcd
membership, OpenBao seal material, LINSTOR data pool, public-edge origin, or
persistent sensitive data, so neither Secure Boot enrollment nor TPM-backed
system-volume encryption is required.

Current node:

| Node | Latitude server | Public IP | Private IP | Talos disk serial | Staging disk serial |
|---|---|---|---|---|---|
| `ash-worker0` | `sv_EvjLaBxRQNoqy` | `206.223.228.99` | `10.8.0.14` | `362510FCEFF6` | `362510FCEFD5` |

NVMe names are observations, never identities: this host has exchanged
`nvme0n1` and `nvme1n1` assignments across reboots. Every destructive command
must resolve and re-check the serial immediately before it runs.

## Preconditions

1. Work from a revision merged to `main`; node configuration and asset hashes
   are Git-owned.
2. Confirm the three control planes, Flux, the cluster API, and the WUM workload
   are healthy away from this node.
3. Restore the Talm mint root to tmpfs as described in `cert-rotation.md`.
4. Use the Talos version, Image Factory schematic, installer digest, raw-image
   URL, and checksum declared in `talm/wum-worker-assets.yaml`. If the factory
   URL returns different bytes, stop and review the changed artifact in a PR.
5. In firmware, set Secure Boot to **Disabled**. Do not clear or enroll keys;
   the worker profile does not use them.

Drain the old node before taking it down:

```sh
kubectl cordon ash-worker0
kubectl drain ash-worker0 --ignore-daemonsets --delete-emptydir-data
```

After the kubelet is down, delete the old Kubernetes Node object. The new
kubelet must create a fresh object so its registration-time dedicated taint is
accepted by the API server:

```sh
kubectl delete node ash-worker0
```

Do not run `talosctl etcd leave`, change LINSTOR assignments, recover OpenBao,
or modify public-edge routing for this worker.

## Wipe and install the Talos disk

Boot a trusted rescue environment or regular Talos maintenance media. Inspect
both model and serial, then bind the target device name only for the lifetime of
that boot. This example deliberately fails closed if the serial does not match:

```sh
lsblk -d -o NAME,MODEL,SERIAL,SIZE

TARGET=/dev/nvme0n1 # replace with the device currently reporting the serial below
test "$(lsblk -dn -o SERIAL "$TARGET" | tr -d ' ')" = "362510FCEFF6" || exit 1
```

Download the declared regular raw disk image and verify it before touching the
disk:

```sh
curl --fail --location --output /tmp/metal-amd64.raw.xz \
  https://factory.talos.dev/image/be66fdc8a38c2f517f33cba0a6daa7ab97ff87d51e8ca7d2160e45911ba09cf5/v1.13.6/metal-amd64.raw.xz
echo '86a0e2cd51351096a682394d201a72ae23be34a1c063ddeeeca8dc4127cc0cd3  /tmp/metal-amd64.raw.xz' | sha256sum --check
xz --test /tmp/metal-amd64.raw.xz
```

Re-run the serial assertion, discard the old Talos contents, and write the
verified ordinary Talos image:

```sh
test "$(lsblk -dn -o SERIAL "$TARGET" | tr -d ' ')" = "362510FCEFF6" || exit 1
blkdiscard --force "$TARGET"
xz --decompress --stdout /tmp/metal-amd64.raw.xz | dd of="$TARGET" bs=16M status=progress conv=fsync
sync
```

Boot the serial-selected Talos disk. In maintenance mode, apply the generated
worker base plus its identity/network overlay from the Talm mint root:

```sh
MINT=/dev/shm/guardian-talm-mint

talm apply --root "$MINT" --talosconfig "$MINT/talosconfig" \
  --endpoints 206.223.228.99 --nodes 206.223.228.99 \
  -f "$MINT/nodes/ash-worker0.yaml" \
  -f "$MINT/nodes/ash-worker0-overlay.yaml" \
  --insecure --skip-resource-validation
```

The overlay pins the ordinary `metal-installer` image and registers the node
with `--node-labels` and `--register-with-taints`. Do not move the dedicated
taint into `machine.nodeTaints`: Talos' dynamic NodeApply controller is not
authorized to add a taint to its own existing Kubernetes Node.

If the machine ever joins before the registration flags are present, cordon and
drain it, reboot it, delete the Node object while the kubelet is down, and let
the configured kubelet recreate the object.

## Wipe the staging disk

Keep the staging disk intact until the new Talos disk has booted with an
authenticated API, the serial-selected system disk is confirmed, and the node
has joined successfully. Then locate the staging serial again in Talos' current
disk inventory and wipe that device. The device name below is only an example:

```sh
talosctl --endpoints 206.223.228.99 --nodes 206.223.228.99 get disks -o yaml

# After verifying that nvme0n1 currently has serial 362510FCEFD5:
talosctl --endpoints 206.223.228.99 --nodes 206.223.228.99 \
  wipe disk nvme0n1 --method ZEROES
```

Power-cycle once after the wipe. Re-resolve both serials and confirm the staging
disk exposes no partitions or filesystems. Never infer this from its old NVMe
name.

The `chunkies` user volume (WAL segments and checkpoints for the worlds this
node serves) also lives on the staging serial and is destroyed by the
reinstall. That is accepted: worlds re-seed from the control plane. Talos holds
the volume pending while foreign data occupies the disk, so after the wipe and
power-cycle, re-apply the machine config
(`talm apply -f nodes/ash-worker0.yaml -f nodes/ash-worker0-overlay.yaml`) and
confirm the volume provisions:

```sh
talosctl --endpoints 206.223.228.99 --nodes 206.223.228.99 \
  get volumestatus u-chunkies
```

## Readiness gates

The rebuild is complete only when all of these pass after the full power cycle:

- Authenticated `machinestatus` reports `stage: running` and `ready: true`.
- Authenticated `securitystate` reports `secureBoot: false`; the kernel still
  reports module-signature enforcement.
- The Talos system disk has serial `362510FCEFF6`; serial `362510FCEFD5`
  carries the provisioned `chunkies` user volume (`talosctl get volumestatus
  u-chunkies` reports ready; the disk shows a LUKS2 volume, never a bare or
  foreign filesystem).
- Kubernetes reports `ash-worker0` Ready with label
  `guardian.dev/dedicated=wum`, exactly the intended
  `guardian.dev/dedicated=wum:NoSchedule` taint, and no shutdown/network taints.
- The node advertises `10.8.0.14` on VLAN 2140, reaches the private API VIP, and
  the Kube-OVN CNI, pinger, and OVS/OVN DaemonSets are Ready on the node.
- The machine configuration contains no `RawVolumeConfig`, no
  `r-guardian-data`, and no TPM encryption override for `STATE` or `EPHEMERAL`;
  its only volume document is the `chunkies` `UserVolumeConfig`.
- No etcd member, OpenBao static-seal label, LINSTOR `data` pool, or public-edge
  origin is assigned to the worker.
- No failed or shutdown pods remain attributed to the node.

Workload placement or traffic cutover is a separate GitOps change. Do not move
WUM merely to prove that the rebuilt node is healthy.

Treat a failed gate as an incomplete rebuild. Keep the node cordoned or absent
from the cluster until the failed condition is corrected.

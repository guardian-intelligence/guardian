# Encrypted node replacement drill

This drill replaces one `guardian-mgmt` node while preserving etcd quorum,
public-edge service, and two current copies of every replicated LINSTOR
volume. The replacement boots the factory Sidero-signed Talos UKI and
provisions new TPM-backed LUKS2 containers for `STATE`, `EPHEMERAL`, and
`r-guardian-data`.

Run exactly one node at a time. Do not begin another replacement until the
node, etcd, CNI, LINSTOR, OpenBao, and public edge have all passed their
recovery gates.

Node map:

| Node | Public IP | Private IP |
|---|---|---|
| `ash-earth` | `206.223.228.101` | `10.8.0.11` |
| `ash-wind` | `45.250.254.119` | `10.8.0.12` |
| `ash-water` | `206.223.228.87` | `10.8.0.13` |

## Preconditions

1. Restore custody to tmpfs and assemble the Talm mint root described in
   `cert-rotation.md`.
2. Confirm all nodes and Flux resources are Ready:

   ```sh
   kubectl get nodes
   tools/ops/cluster-watch --status
   ```

3. Confirm etcd has three voting members.
4. Confirm all three LINSTOR satellites are Online, all storage pools are
   `Ok`, and no DRBD replica is below `UpToDate`:

   ```sh
   kubectl -n cozy-linstor exec deploy/linstor-controller -- linstor node list
   kubectl -n cozy-linstor exec deploy/linstor-controller -- linstor storage-pool list
   kubectl -n cozy-linstor exec deploy/linstor-controller -- linstor resource list --faulty
   ```

5. Export the target node's LINSTOR assignments. A replicated assignment may
   be removed only after both other nodes report `UpToDate` for that resource.
   Record local `LUKS,STORAGE` assignments separately because they are rebuilt
   empty rather than resynchronized.
6. Run a fresh `talos-backup` Job and require it to complete.
7. Disable the target origin in the declared
   `bootstrap/guardian-mgmt-dns` pool, apply only that pool, and wait until
   Cloudflare reports the two remaining origins healthy. Run
   `aspect infra edge-health` before the node operation.

## Remove the node

Set these variables for the one target:

```sh
MINT=/dev/shm/guardian-talm-mint
NODE=<ash-earth|ash-wind|ash-water>
PUBLIC_IP=<node-public-ip>
PRIVATE_IP=<node-private-ip>
```

Drain workload traffic, remove the etcd member cleanly, and remove the old
Kubernetes Node object:

```sh
kubectl cordon "$NODE"
kubectl drain "$NODE" --ignore-daemonsets --delete-emptydir-data

talosctl --talosconfig "$MINT/talosconfig" \
  --endpoints <healthy-public-ip> --nodes "$PRIVATE_IP" etcd leave

kubectl delete node "$NODE"
```

If the node is already dark, remove its member ID from a healthy Talos
endpoint with `talosctl etcd remove-member <id>`, then delete the Kubernetes
Node object.

## Wipe and reprovision

Read the parent device of `r-guardian-data` before resetting:

```sh
DATA_DISK=$(
  talosctl --talosconfig "$MINT/talosconfig" \
    --endpoints "$PUBLIC_IP" --nodes "$PRIVATE_IP" \
    get volumestatus r-guardian-data -o json |
    jq -r .spec.parentLocation
)
```

Wipe only the encrypted system partitions and the data disk. EFI and META
remain intact, and the UEFI PK/KEK/`db` enrollment is firmware state rather
than disk state:

```sh
talosctl --talosconfig "$MINT/talosconfig" \
  --endpoints "$PUBLIC_IP" --nodes "$PRIVATE_IP" \
  reset --graceful=false \
  --system-labels-to-wipe STATE,EPHEMERAL \
  --user-disks-to-wipe "$DATA_DISK" \
  --reboot --wait=false
```

Apply the complete node configuration in maintenance mode. The overlay is
required because it owns the node identity, VLAN, MTU, and raw-volume disk
serial:

```sh
talm apply --root "$MINT" --talosconfig "$MINT/talosconfig" \
  --endpoints "$PUBLIC_IP" --nodes "$PUBLIC_IP" \
  -f "$MINT/nodes/$NODE.yaml" \
  -f "$MINT/nodes/$NODE-overlay.yaml" \
  --insecure --skip-resource-validation
```

The pinned installer is
`factory.talos.dev/metal-installer-secureboot/...`; a non-Secure-Boot
installer is not an acceptable substitute.

Wait for the authenticated Talos API and Kubernetes Node readiness. Confirm
that `securitystate` reports Secure Boot, UKI boot, and module-signature
enforcement before proceeding:

```sh
talosctl --talosconfig "$MINT/talosconfig" \
  --endpoints <healthy-public-ip> --nodes "$PRIVATE_IP" \
  get securitystate -o yaml

kubectl wait node "$NODE" --for=condition=Ready --timeout=15m
```

## Reconcile the Kube-OVN node gateway

The node annotation, `ovn0` interface, and OVN northbound logical switch
port must carry the same MAC and join-subnet IP. This gate is especially
important when the OVN database has also been restored.

```sh
NODE_MAC=$(kubectl get node "$NODE" \
  -o jsonpath='{.metadata.annotations.ovn\.kubernetes\.io/mac_address}')
NODE_JOIN_IP=$(kubectl get node "$NODE" \
  -o jsonpath='{.metadata.annotations.ovn\.kubernetes\.io/ip_address}')

OVN_CENTRAL=$(
  kubectl -n cozy-kubeovn get pod -l app=ovn-central -o name |
  while read -r pod; do
    if kubectl -n cozy-kubeovn exec "$pod" -- \
      ovn-appctl -t /var/run/ovn/ovnnb_db.ctl \
      cluster/status OVN_Northbound 2>/dev/null |
      grep -q '^Role: leader$'; then
      echo "${pod#pod/}"
      break
    fi
  done
)

kubectl -n cozy-kubeovn exec "$OVN_CENTRAL" -- \
  ovn-nbctl --no-leader-only \
  --db=unix:/var/run/ovn/ovnnb_db.sock \
  lsp-get-addresses "node-$NODE"
```

If that address differs from `"$NODE_MAC $NODE_JOIN_IP"`, reconcile the
dynamic OVN port to the Kubernetes Node identity:

```sh
kubectl -n cozy-kubeovn exec "$OVN_CENTRAL" -- \
  ovn-nbctl --no-leader-only \
  --db=unix:/var/run/ovn/ovnnb_db.sock \
  lsp-set-addresses "node-$NODE" "$NODE_MAC $NODE_JOIN_IP"
```

Wait for the southbound port binding to report the same address, then prove
DNS and TCP 443 egress from a pod scheduled on the replacement node before
allowing database recovery or backup jobs to proceed.

## Recreate the encrypted LINSTOR pool

The LINSTOR controller still records the node's old assignments and storage
pool, while the underlying LVM metadata was wiped. Reconcile in this order:

1. Require two `UpToDate` copies on the surviving nodes for every replicated
   resource in the saved inventory.
2. Toggle each target-node DRBD assignment to diskless:

   ```sh
   kubectl -n cozy-linstor exec deploy/linstor-controller -- \
     linstor resource toggle-disk "$NODE" <resource> --diskless
   ```

3. Delete the target-node assignments for saved local
   `LUKS,STORAGE` resources. Their application-level recovery procedure owns
   their contents.
4. Delete the empty stale pool:

   ```sh
   kubectl -n cozy-linstor exec deploy/linstor-controller -- \
     linstor storage-pool delete --quiet "$NODE" data
   ```

5. Wait for
   `LinstorSatelliteConfiguration/guardian-data-pool-$NODE` to recreate the
   `data` volume group and pool on
   `/dev/mapper/luks2-r-guardian-data`. Do not run `pvcreate` or `vgcreate`
   against a raw NVMe device.
6. Reattach DRBD assignments in bounded batches and require every batch to
   become `UpToDate` before starting the next:

   ```sh
   kubectl -n cozy-linstor exec deploy/linstor-controller -- \
     linstor resource toggle-disk "$NODE" <resource> --storage-pool data
   ```

7. Recreate each local resource assignment on the new pool:

   ```sh
   kubectl -n cozy-linstor exec deploy/linstor-controller -- \
     linstor resource create "$NODE" <resource> --storage-pool data
   ```

Use the Piraeus custom-resource troubleshooting procedure if the operator
does not recreate the pool:
<https://cozystack.io/docs/v1.5/operations/troubleshooting/piraeus-custom-resources/>.

## Restore the OpenBao member

OpenBao uses two node-local `local-encrypted-retain` PVCs per ordinal. The
replacement volumes are empty by design; Raft restores current data from the
other members.

Place the custodied static seal key using the two-phase debug-pod procedure
in `cold-boot-bootstrap.md`. Verify only its fingerprint and length, delete
the debugger pod, and wait for the OpenBao member to become Ready and
unsealed.

Run:

```sh
aspect infra openbao-drill --kubeconfig <kubeconfig> \
  --kube-api-server <reachable-api-server>
```

## Recovery gates

The node is recovered only when all of the following pass:

- `securitystate`: Secure Boot, UKI, and module-signature enforcement are
  true.
- `STATE`, `EPHEMERAL`, and `r-guardian-data`: `ready`, LUKS2, TPM key,
  PCR 7, signed PCR 11 policy; the latter two are locked to `STATE`.
- Kubernetes Node Ready and API `/readyz` successful.
- Kube-OVN CNI and pinger Ready on the node; the Node annotation,
  `ovn0`, northbound logical switch port, and southbound port binding agree,
  and pod DNS plus TCP 443 egress succeeds.
- LINSTOR satellite Online, pool `Ok`, no faulty resources, all restored
  DRBD assignments `UpToDate`.
- OpenBao drill passes one shared three-member cluster ID.
- Flux Kustomizations and HelmReleases Ready.
- Cloudflare origin re-enabled through the declared pool and reported
  healthy.
- `aspect infra edge-health` passes.

## Replacement hardware or a missing EFI partition

Use the exact ISO and hashes in
`src/infrastructure/talm/secureboot-assets.yaml`. Put a new board in UEFI
Setup mode, boot the pinned factory Sidero Secure Boot ISO, and let it enroll
the declared PK/KEK/`db` set. Firmware that already has the declared keys
does not need re-enrollment.

The managed trust database excludes well-known Microsoft certificates, so a
provider's Microsoft-signed stock or rescue image is not a recovery
dependency. Deliver the pinned Sidero-signed ISO through KVM virtual media or
a minimal iPXE transport. iPXE is transport only; UEFI still verifies the
Sidero-signed UKI.

Never change firmware or Secure Boot enrollment on two nodes at once. Any
PCR 7 change can prevent TPM unseal and must be handled as loss of that node.

# WUM worker reinstall

This procedure installs or replaces a disposable regional WUM worker in the
`guardian-mgmt` Kubernetes cluster. The worker boots the same Sidero-signed
Talos UKI as the management nodes, but it has no etcd membership, public-edge
origin, OpenBao seal material, LINSTOR data pool, or persistent sensitive data.
Its `STATE` and `EPHEMERAL` volumes therefore use Talos defaults rather than
TPM-backed LUKS2.

Current node:

| Node | Latitude server | Public IP | Private IP | Install-disk serial |
|---|---|---|---|---|
| `ash-worker0` | `sv_EvjLaBxRQNoqy` | `206.223.228.99` | `10.8.0.14` | `362510FCEFF6` |

## Preconditions

1. Work from a revision merged to `main`; node configuration is Git-owned.
2. Confirm the three control planes, Flux, and the cluster API are Ready.
3. Restore the Talm mint root to tmpfs as described in `cert-rotation.md`.
4. Download the ISO named in `talm/secureboot-assets.yaml` once and verify its
   SHA-256 before attaching or serving it. If the factory URL returns different
   bytes, stop and review the new ISO, enrollment payloads, signer certificate,
   and hashes in a PR; never silently bless drift during a node operation.
5. Resolve disks by serial. `/dev/nvme*` enumeration is not stable across boots.
   The installer must select `362510FCEFF6`; the other disk is not part of the
   Talos worker profile.

If an old Kubernetes Node object exists, drain and remove only that object:

```sh
kubectl cordon ash-worker0
kubectl drain ash-worker0 --ignore-daemonsets --delete-emptydir-data
kubectl delete node ash-worker0
```

Do not run `talosctl etcd leave`, LINSTOR assignment changes, OpenBao recovery,
or Cloudflare-origin changes for this worker.

## Enroll Secure Boot on new or reset firmware

Skip enrollment only when the firmware already trusts the Sidero certificate
declared in `secureboot-assets.yaml` and a Talos maintenance boot reports
`secureBoot: true`.

1. Put the board in UEFI Setup Mode by clearing its current Secure Boot keys.
2. Boot the complete verified `metal-amd64-secureboot.iso` through KVM virtual
   media or a minimal iPXE transport. Do not direct-boot only the UKI for first
   enrollment; that bypasses the ISO boot menu.
3. On bare metal, press Esc at the ISO boot menu and select
   `Enroll Secure Boot keys: auto`. The stock ISO's automatic `if-safe` policy
   does not enroll keys unattended on bare metal.
4. Reboot the ISO and require this maintenance-mode result before installing:

   ```sh
   talosctl --endpoints 206.223.228.99 --nodes 206.223.228.99 \
     get securitystate --insecure -o yaml
   ```

   `secureBoot`, `bootedWithUKI`, and `moduleSignatureEnforced` must be true,
   and the PCR signer must match `secureboot-assets.yaml`.

## Install the worker

Apply the generated worker base and its identity/network overlay from the Talm
mint root:

```sh
MINT=/dev/shm/guardian-talm-mint

talm apply --root "$MINT" --talosconfig "$MINT/talosconfig" \
  --endpoints 206.223.228.99 --nodes 206.223.228.99 \
  -f "$MINT/nodes/ash-worker0.yaml" \
  -f "$MINT/nodes/ash-worker0-overlay.yaml" \
  --insecure --skip-resource-validation
```

Detach the ISO after the install starts. Talos installs the digest-pinned
`metal-installer-secureboot` image to the serial-selected system disk and
reboots into the installed UKI. Do not bootstrap etcd from a worker.

## Readiness gates

The worker is ready only when all of these pass:

- Authenticated `securitystate` reports Secure Boot, UKI boot, module-signature
  enforcement, and the declared PCR signer.
- Kubernetes reports `ash-worker0` Ready with label
  `guardian.dev/dedicated=wum` and taint
  `guardian.dev/dedicated=wum:NoSchedule`.
- The node advertises `10.8.0.14` on VLAN 2140, reaches the private API VIP,
  and Kube-OVN plus pod DNS/TCP egress are healthy.
- The machine configuration contains no `RawVolumeConfig`, no
  `r-guardian-data`, and no TPM encryption override for `STATE` or
  `EPHEMERAL`.
- No etcd member, OpenBao static-seal label, LINSTOR `data` pool, or
  Cloudflare public-edge origin is assigned to the worker.
- The WUM workload is scheduled only through its dedicated toleration and its
  WebTransport/QUIC UDP 4433 endpoint passes the live service check.

Treat a failed gate as an incomplete rebuild. Keep the node drained or absent
from the cluster until the failed condition is corrected.

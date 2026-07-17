# Management cluster trusted boot and data at rest

This document is the current control description for the three-node
`guardian-mgmt` cluster. It covers the boot trust chain, local NVMe
encryption, LINSTOR volume encryption, synthetic-data exceptions, and the
evidence an operator or auditor can collect.

## Boot trust

All three nodes run Talos from a Secure Boot UKI produced by the Sidero Labs
Image Factory. UEFI Secure Boot is enabled in User mode. The factory
Secure Boot ISO enrolled the Sidero Labs PK, KEK, and `db` authorization
files while each board was in Setup mode.

The managed trust path excludes well-known UEFI certificates. In particular,
the enrollment is not generated with
`--include-well-known-uefi-certs`; Microsoft-signed rescue media is not part
of the declared recovery path. Recovery media must be the pinned,
Sidero-signed ISO or another artifact signed by a key already in the managed
UEFI database.

[`secureboot-assets.yaml`](../src/infrastructure/talm/secureboot-assets.yaml)
records the exact Talos version, Image Factory schematic, ISO and installer
digests, Sidero signing-certificate fingerprint, PCR-signing public-key
fingerprint, PK/KEK/`db` authorization-file hashes, and the explicit
well-known-certificate exclusion. Guardian trusts Sidero's signing service
for this factory path and does not hold the corresponding private boot
signing key.

The required runtime state on every node is:

- `secureBoot: true`
- `bootedWithUKI: true`
- `moduleSignatureEnforced: true`
- the PCR-signing fingerprint declared in `secureboot-assets.yaml`

PCR 7 binds TPM unseal to Secure Boot state and the enrolled PK/KEK/`db`
set. The UKI carries a policy signed by the declared PCR-signing key and
binds the boot measurement in PCR 11. A firmware update, Secure Boot
configuration change, or UEFI database change can therefore make a node
unable to unseal its volumes. Treat that outcome as node loss: rebuild one
node, restore its static seal key, and resynchronize storage before touching
another node.

Reference:
[Talos SecureBoot](https://docs.siderolabs.com/talos/v1.13/platform-specific-installations/bare-metal-platforms/secureboot).

## Node volume encryption

Talos provisions three LUKS2 volumes on every node:

| Volume | Contents | TPM policy |
|---|---|---|
| `STATE` | Machine identity, configuration, and system state | TPM key; PCR 7 plus signed PCR 11 policy |
| `EPHEMERAL` | `/var`, including Kubernetes and container runtime state | Same policy; locked to `STATE` |
| `r-guardian-data` | The NVMe substrate for the LINSTOR `data` pool | Same policy; locked to `STATE` |

The raw data volume is selected by immutable disk serial in each Talos node
overlay. Piraeus consumes only
`/dev/mapper/luks2-r-guardian-data`; it must never be pointed at a raw
`/dev/nvme*` device or `/dev/disk/by-id/*` path. The mapper is the lower
encryption boundary for every LINSTOR allocation, including explicitly
classified synthetic data.

## Cozystack and LINSTOR encryption

The LINSTOR master passphrase is supplied through
`guardian-linstor-master-passphrase` and `spec.linstorPassphraseSecret`.
Non-synthetic persistent data uses Cozystack's native encrypted class shape:

| StorageClass | LINSTOR layers | Replication | Reclaim |
|---|---|---:|---|
| `local-encrypted` | `luks storage` | 1 | Delete |
| `local-encrypted-retain` | `luks storage` | 1 | Retain |
| `replicated-encrypted` | `drbd luks storage` | 3 | Delete |
| `replicated-encrypted-retain` | `drbd luks storage` | 3 | Retain |

These classes set `linstor.csi.linbit.com/encryption: "true"` and carry
`guardian.dev/encryption-at-rest=linstor-luks`. The replicated encrypted
class is the cluster default. TigerBeetle and every other system containing
customer transactions, balances, credentials, or business state must use an
encrypted class; its deployment must not select a `synthetic-*` class.

Synthetic-only classes deliberately omit the per-volume LINSTOR LUKS layer
to keep the exception visible:

- Their names begin with `synthetic-`.
- They set `linstor.csi.linbit.com/encryption: "false"`.
- They carry `guardian.dev/data-classification=synthetic`.
- They carry
  `guardian.dev/encryption-at-rest=talos-luks2-raw-volume`, because their
  backing pool remains inside the Talos-encrypted raw volume.
- A fail-closed admission policy denies a PVC using a `synthetic-*` class
  unless the PVC has the same synthetic-data label.

Reference:
[Cozystack disk encryption](https://cozystack.io/docs/v1.5/storage/disk-encryption/).

## Backup boundary

R2 automatically encrypts every object and its metadata at rest with
AES-256-GCM using Cloudflare-managed keys. Workload backup procedures may
add client-side encryption where their runbook declares it; etcd snapshots,
for example, are age-encrypted before upload. R2 encryption is a separate
provider control and does not replace the node and LINSTOR controls above.

Reference:
[Cloudflare R2 data security](https://developers.cloudflare.com/r2/reference/data-security/).

## Verification

Use an authenticated Talos endpoint and query all three internal node
addresses through it:

```sh
talosctl --talosconfig <talosconfig> --endpoints <endpoint> \
  --nodes 10.8.0.11,10.8.0.12,10.8.0.13 \
  get securitystate -o yaml

for volume in STATE EPHEMERAL r-guardian-data; do
  talosctl --talosconfig <talosconfig> --endpoints <endpoint> \
    --nodes 10.8.0.11,10.8.0.12,10.8.0.13 \
    get volumestatus "$volume" -o yaml
done
```

Each volume must be `ready`, use `encryptionProvider: luks2`, list only the
TPM key, bind PCR 7, and report PCR 11 as the public-key policy PCR.
`EPHEMERAL` and `r-guardian-data` must also report
`encryptionLockedToState: true`.

For the storage plane:

```sh
kubectl -n cozy-linstor exec deploy/linstor-controller -- linstor node list
kubectl -n cozy-linstor exec deploy/linstor-controller -- linstor storage-pool list
kubectl -n cozy-linstor exec deploy/linstor-controller -- linstor resource list --faulty
kubectl get storageclass
```

All satellites and pools must be `Online`/`Ok`, the faulty-resource list
must be empty, and no unclassified plaintext StorageClass may exist.

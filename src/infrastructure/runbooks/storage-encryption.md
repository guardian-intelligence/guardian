# Storage encryption

Guardian uses two encryption controls with non-overlapping scope:

- Talos encrypts `STATE` and `EPHEMERAL` with LUKS2 keys sealed to TPM PCR 7.
  `STATE` contains etcd and Kubernetes Secrets; `EPHEMERAL` contains container
  runtime and node-local workload state.
- Cozystack encrypts persistent volumes with LINSTOR's native LUKS layer.
  Piraeus continues to consume the stable raw NVMe device IDs declared in
  `base/storage/linstor-data-pools.yaml`; encryption is selected per PVC by its
  StorageClass.

Secure Boot must report enabled before Talos provisions either system volume.
The machine configuration sets `checkSecurebootStatusOnEnroll: true`, so a
node fails closed instead of sealing a disk key to an unverified boot state.

## Storage classes

| StorageClass | LINSTOR layers | Replicas | Reclaim | Intended data |
| --- | --- | ---: | --- | --- |
| `local-encrypted` | `luks storage` | 1 | Delete | Rebuildable node-local state |
| `local-encrypted-retain` | `luks storage` | 1 | Retain | Application-replicated durable state, including OpenBao raft and audit data |
| `replicated-encrypted` | `drbd luks storage` | 3 | Delete | Durable platform and product data |
| `replicated-encrypted-retain` | `drbd luks storage` | 3 | Retain | Customer financial ledgers and other records requiring explicit retention |
| `synthetic-local` / `synthetic-local-retain` | `storage` | 1 | Delete / Retain | Explicitly labeled synthetic data only |
| `synthetic-replicated` / `synthetic-replicated-retain` | `drbd storage` | 3 | Delete / Retain | Explicitly labeled synthetic data only |

`replicated-encrypted` is the cluster default. A PVC selecting a `synthetic-*`
class must carry `guardian.dev/data-classification=synthetic`; admission denies
an unlabeled claim.

TigerBeetle uses `replicated-encrypted-retain` from its first PVC. Its
transaction and balance volumes are therefore distinguishable from synthetic
test data by both StorageClass and PVC classification.

## LINSTOR master passphrase

The native LUKS layer has one LINSTOR master passphrase. Losing it makes every
encrypted LINSTOR volume unrecoverable.

- Offline source of truth: `linstor/master-passphrase` in the encrypted custody
  bundle.
- Runtime copy: immutable Kubernetes Secret
  `cozy-linstor/guardian-linstor-master-passphrase`, key
  `MASTER_PASSPHRASE`.
- Controller wiring: the Cozystack-rendered `LinstorCluster` carries
  `spec.linstorPassphraseSecret: guardian-linstor-master-passphrase` through a
  Flux Helm post-renderer.

The runtime copy deliberately does not come from OpenBao: OpenBao's own PVCs
need LINSTOR to unlock before OpenBao can start. Talos `STATE` LUKS protects
the etcd copy, etcd snapshots preserve it, and custody provides the independent
from-nothing recovery copy.

Materialize the runtime Secret only during an audited platform-admin ceremony.
The passphrase never rides argv or Git:

```sh
aspect infra custody --action restore
aspect infra custody --action linstor-generate
aspect infra custody --action create --yes
BUNDLE=/dev/shm/guardian-custody
test -s "$BUNDLE/linstor/master-passphrase"
kubectl -n cozy-linstor create secret generic guardian-linstor-master-passphrase \
  --from-file=MASTER_PASSPHRASE="$BUNDLE/linstor/master-passphrase" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n cozy-linstor patch secret guardian-linstor-master-passphrase \
  --type=merge -p '{"immutable":true}'
```

The custody snapshot and offsite copy must complete before any encrypted PVC
is provisioned. Wipe the plaintext bundle immediately after the ceremony.

## Verification

After every controller restart or node recovery:

1. Verify all nodes report `securityState.secureBoot=true` and
   `bootedWithUKI=true`.
2. Verify `VolumeStatus/STATE` and `VolumeStatus/EPHEMERAL` are ready and their
   configuration reports LUKS2 TPM encryption.
3. Verify `LinstorCluster/linstorcluster` references
   `guardian-linstor-master-passphrase` and the controller Deployment is ready.
4. Provision one disposable claim from `local-encrypted` and one from
   `replicated-encrypted`, write and read a sentinel, and verify the LINSTOR
   resource layer stack includes `LUKS` (and `DRBD` for the replicated claim).
5. Verify every non-synthetic bound PVC uses an encrypted class and every
   synthetic PVC carries the required classification label.
6. Run a fresh etcd backup and the application backup/restore drills before
   deleting any pre-migration volume.

Changing a StorageClass does not transform an existing volume. Migrate an
existing PVC by restoring or copying application-consistent data into a newly
provisioned encrypted PVC, validating the application, and only then deleting
the original resource.

## Piraeus custom-resource break glass

Normal encryption changes converge through Flux and the Cozystack-owned
`LinstorCluster`. If a disturbed Piraeus system leaves its custom resources
uneditable, follow Cozystack's documented recovery sequence:

1. Suspend the GitOps owner of the affected Piraeus release.
2. Temporarily remove or break the Piraeus validating webhook selector.
3. Scale `piraeus-operator-controller-manager` to zero when its reconciliation
   would recreate the resource being repaired.
4. Repair the custom resource; remove finalizers only when deleting a resource
   whose controller cannot complete cleanup.
5. Restore the webhook selector, scale the operator to one, resume GitOps, and
   wait for full Piraeus and Flux health.

The webhook and operator must never remain disabled. This is recovery for stuck
custom resources, not part of passphrase setup or routine volume provisioning.

References:

- <https://cozystack.io/docs/v1.5/storage/disk-encryption/>
- <https://cozystack.io/docs/v1.5/operations/troubleshooting/piraeus-custom-resources/>
- <https://piraeus.io/docs/v2.10.5/reference/linstorcluster/>

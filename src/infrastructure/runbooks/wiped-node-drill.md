# Wiped-node recovery drill

This drill proves that one completely lost management node can return from
Git, custody, the two surviving members, and off-cluster backups. Run exactly
one node at a time by explicit IP. Do not start the next node until Kubernetes,
etcd, LINSTOR/DRBD, OpenBao, Flux, and the public edge have fully recovered.

Stop the drill immediately if the public edge is disrupted for more than 60
seconds.

| Node | Public IP | Private IP | System NVMe serial | LINSTOR NVMe serial |
| --- | --- | --- | --- | --- |
| `ash-earth` | `206.223.228.101` | `10.8.0.11` | `362510FCEFB8` | `362510FD7C47` |
| `ash-wind` | `45.250.254.119` | `10.8.0.12` | `352410A4E051` | `352410A4E0A6` |
| `ash-water` | `206.223.228.87` | `10.8.0.13` | `362510FE3218` | `362510FE3204` |

## Preconditions

- `tools/ops/cluster-watch --status --until-ready` is green.
- All three nodes are Ready; etcd has three healthy members; all DRBD devices
  are `UpToDate`; OpenBao has one active and two healthy standby members.
- Every node reports `secureBoot=true` and `bootedWithUKI=true` through Talos
  `SecurityState`.
- `VolumeStatus/STATE` and `VolumeStatus/EPHEMERAL` are ready and use LUKS2
  TPM keys sealed to PCR 7.
- `LinstorCluster/linstorcluster` references the immutable
  `guardian-linstor-master-passphrase` Secret, and an encrypted local and
  replicated canary PVC both pass the sentinel test in `storage-encryption.md`.
- The custody repository verifies, its latest snapshot contains
  `linstor/master-passphrase`, and the bundle is restored on tmpfs for the
  duration of the ceremony.
- A fresh etcd snapshot exists in R2. The custody snapshot contains the
  OpenBao static seal key, every required importer input, and each durable
  Transit `transit/backup` export; the reinit/import drill has passed.
- The signed Talos recovery ISO matches the SHA-256 recorded in
  `talm/image-factory-schematic.yaml`; its EFI bootloader and UKI signatures
  validate against the enrolled Sidero Labs certificate.
- The node's BIOS/BMC firmware matches the approved cluster baseline. UEFI
  mode, TPM 2.0, IOMMU, and Secure Boot are enabled; CSM is disabled.
- A one-second public-edge probe is running from outside the cluster and
  records status plus latency. Any continuous non-200 window over 60 seconds
  is a hard stop.
- For planned work, remove the selected origin through the declared Cloudflare
  load-balancer configuration, converge it, and prove 60 seconds of clean
  traffic before resetting the node.

## Recovery sequence

Set the node explicitly in every command:

```sh
NODE=ash-earth
PUBLIC_IP=206.223.228.101
OTHER_IP=45.250.254.119
MINT=/dev/shm/guardian-talm-mint
```

1. Reset or replace the selected node. A full-loss drill includes both the
   system and LINSTOR NVMe devices; ordinary Secure Boot maintenance wipes only
   the system device selected by its serial.
2. From a surviving node, inspect etcd membership, remove only the dead member,
   and delete only the dead Kubernetes Node object.
3. Enter UEFI Setup Mode through the BMC console. Mount the custody-verified
   Talos Secure Boot ISO as virtual media, boot it, and enroll the Talos keys
   with the ISO's automatic enrollment action. Reboot the ISO under enforced
   Secure Boot and verify maintenance-mode `SecurityState` reports
   `secureBoot=true` before installing.
4. Apply the node configuration from the custody-assembled Talm root. The base
   file and per-node overlay are one configuration chain; never omit the
   hostname/VLAN/MTU overlay.

   ```sh
   talm apply --talosconfig "$MINT/talosconfig" \
     -f "$MINT/nodes/$NODE.yaml" -f "$MINT/nodes/$NODE-overlay.yaml" \
     --insecure
   ```

   The pinned system-disk serial must match the intended device. The installer
   must be the digest-pinned `metal-installer-secureboot` image from
   `talm/values.yaml`.
5. Detach virtual media and boot from disk. Verify, in order:

   - signed UKI boot with `secureBoot=true` and `bootedWithUKI=true`;
   - `STATE` and `EPHEMERAL` ready with LUKS2 TPM encryption;
   - the exact node Ready with the expected private and public addresses;
   - watchdog active; etcd membership returns to three healthy members.

6. Restore the LINSTOR storage pool:

   - If the original data NVMe survived, do not format it. Wait for the
     Satellite and controller to rediscover the existing `data` volume group.
   - If the data device was lost, update its stable by-id serial in Git before
     attaching the replacement. Let the Piraeus
     `LinstorSatelliteConfiguration` create the `data` volume group. Remove a
     stale LINSTOR node or storage-pool record only through the audited
     break-glass procedure when it prevents operator reconciliation.
   - Wait until every three-way resource is present and `UpToDate`; no
     `Unknown`, `Inconsistent`, or unintentional `Diskless` device may remain.

7. Restore the node's OpenBao member after LINSTOR is healthy. A lost local
   OpenBao PVC is replaced with `local-encrypted-retain`, the custody-held
   static seal key is placed on the node, and the member rejoins the surviving
   raft leader. Verify all members share one `cluster_id`, exactly one is
   active, and none is sealed. Never copy another member's raft directory.
8. Verify Flux convergence, all encrypted PVCs, storage alerts, the public
   edge, DNS, TLS, OpenBao/ESO reads, Postgres backup health, and R2 backup
   freshness. Keep the node out of the public origin pool until all gates pass.
9. Re-enable the origin through Git, converge Cloudflare, and observe clean
   public traffic plus alerting for at least 15 minutes. Wipe the restored
   custody bundle.

## Piraeus break glass

If Piraeus custom resources are stuck and the validating webhook prevents
repair, use the sequence in `storage-encryption.md`: suspend the GitOps owner,
temporarily disable the webhook and operator, make the smallest repair, then
restore the webhook/operator and resume GitOps. Do not leave finalizers,
webhooks, or controllers disabled.

## Acceptance record

Record the node, start/end timestamps, Secure Boot evidence, system-volume
encryption evidence, LINSTOR resync duration, OpenBao raft state, public-edge
non-200 seconds, Flux revision, backup identifiers, and alert state in the
audited drill evidence store. The repository documents the current procedure;
individual drill history belongs in the evidence store.

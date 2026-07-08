# etcd snapshot restore

Restores guardian-mgmt cluster state from a talos-backup snapshot in R2.
This is the middle rung of the recovery ladder: above "the cluster is
degraded but etcd quorum holds" (fix the cluster, no restore) and below
"rebuild everything from Git + custody" (cold-boot runbook). Use it when
etcd state is lost or corrupted beyond quorum recovery but the machines
are otherwise fine, or when you need to roll the whole control plane back
to a point in time.

What a snapshot does NOT contain: PVC data (CNPG/ClickHouse have their own
R2 backup chains — see backup-audit.md), workload images, or anything on
the ephemeral partitions. What it DOES contain: every Kubernetes object at
snapshot time, including Secrets in the clear — which is why snapshots are
age-encrypted and the bucket token is write-scoped to one bucket.

Snapshots land every six hours at
`s3://guardian-backups/talos-etcd/guardian-mgmt/guardian-mgmt-<RFC3339>.snap.zst.age`
(zstd inside age). Freshness is asserted by the `etcd-snapshot-health`
VMRule; a stale or failed snapshot pages.

Node map: ash-earth `206.223.228.101`, ash-wind `45.250.254.119`,
ash-water `206.223.228.87`.

## Fetch and decrypt

```sh
# 1. Custody holds both the R2 credential source-of-truth and the age
#    identity key (custody.env GUARDIAN_ETCD_SNAPSHOT_AGE_KEY).
aspect infra custody --action restore --yes
. /dev/shm/guardian-custody/custody.env

# 2. List and fetch the newest snapshot (any S3 client; keep it in tmpfs).
#    Endpoint/keys: kv/guardian/guardian-mgmt/tenant-root/backups-r2 values
#    mirrored in custody.env.
oras ...  # or aws-cli/rclone equivalent against the R2 endpoint

# 3. Decrypt + decompress to a raw bbolt db.
printf '%s' "$GUARDIAN_ETCD_SNAPSHOT_AGE_KEY" | age -d -i - \
  guardian-mgmt-<stamp>.snap.zst.age | zstd -d -o db.snapshot
```

## Restore

Talos's etcd recovery flow (docs: "Disaster Recovery"): stop etcd on all
control-plane nodes, then bootstrap one node from the snapshot; the others
rejoin from it.

```sh
MINT=/dev/shm/guardian-talm-mint   # assembled per cert-rotation.md step 1
CP=206.223.228.101,45.250.254.119,206.223.228.87

# 1. Verify the snapshot is intact before touching any node
talosctl --talosconfig "$MINT/talosconfig" etcd snapshot --help >/dev/null
etcdutl snapshot status db.snapshot   # sanity: hash, revision, total keys

# 2. Recover: wipe etcd data on all CP nodes, bootstrap the first from
#    the snapshot (--recover-skip-hash-check only if the snapshot was
#    taken by talos-backup rather than etcdutl; talos-backup snapshots
#    are consistent reads, hash check passes)
talosctl --talosconfig "$MINT/talosconfig" -e 206.223.228.101 -n 206.223.228.101 \
  bootstrap --recover-from=./db.snapshot

# 3. Watch the other members rejoin, then Flux re-reconcile
talosctl --talosconfig "$MINT/talosconfig" -e "$CP" -n "$CP" etcd members
tools/ops/cluster-watch --status
```

Post-restore: anything that changed in the cluster after the snapshot
timestamp is gone; Flux converges Git-declared state forward again, and
the gap that matters is undeclared state (Kargo freight since the
snapshot, cert-manager reissues — both self-heal on their own
reconcile loops). Wipe custody plaintext when done:
`aspect infra custody --action wipe`.

## Retention

R2 currently keeps every snapshot (~4/day). Set a bucket lifecycle rule on
the `talos-etcd/` prefix (30 days is plenty: the restore value of a
snapshot decays fast — older state is better rebuilt from Git). Tracked as
a follow-up; no Guardian-declared owner for R2 bucket config exists yet.

## Drill log

| Date | Result | Notes |
|------|--------|-------|
| _none yet_ | | First restore drill pending: take a live snapshot, restore into a scratch single-node target, assert object counts match. |

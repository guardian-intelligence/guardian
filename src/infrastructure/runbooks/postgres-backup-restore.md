# Postgres Backup And Restore (Cozystack → Cloudflare R2)

Postgres backups run through Cozystack's platform backup machinery: the
`cozy-default` BackupClass routes `apps.cozystack.io/Postgres` to the CNPG
strategy (barman: continuous WAL archiving + base backups, true PITR),
targeting the external `guardian-backups` R2 bucket configured in
`base/cozystack/platform.yaml` (`provisionBucket: false`). Credentials flow
OpenBao → ESO (`Secret/guardian-backups-creds` in `tenant-root`, see
`base/secrets/backup-storage-credentials.yaml`) → the controller's
credentials projector (`cozy-backups-creds` per consumer namespace).

Layout in R2: `s3://guardian-backups/<namespace>/<app>/<namespace>-<app>/`
with `base/` (barman base backups, 30d retention, gzip) and `wals/`.
The bucket carries a 7-day Age bucket lock (< barman's 30d retention, so
pruning still works); deletes of anything younger are refused at the bucket
layer. First proven end to end 2026-07-05 (bot-verified restore with a
post-backup marker row arriving via WAL replay; RestoreJob apply →
Succeeded in 68s, restored cluster healthy ≈ 2min).

## In scope

- `tenant-root/verself-controlplane` — nightly Plan 02:00 UTC (`base/apps/verself-controlplane-postgres.yaml`)
- `tenant-guardian-prod/keycloak` — nightly Plan 03:00 UTC (`deployments/iam/prod/postgres.yaml`)

Beta/gamma Keycloak are deliberately out. Adding an instance is:
`backup: {enabled: true, useSystemBucket: true}` on the app CR, a `Plan`,
and the enablement sequence below.

## Enabling backups on an instance (the two-BackupJob rule)

`barmanObjectStore` is SSA-patched onto the live CNPG Cluster at **first
BackupJob time**, not at merge. Two consequences, both hit live on
2026-07-05:

1. WAL archiving (`archive_command`) is inactive until the first BackupJob —
   never wait for the Plan's cron tick; fire an ad-hoc BackupJob right away.
2. **The first backup is typically unrestorable.** Before the SSA patch
   propagates, CNPG's archiver runs in "not configured" mode: it marks
   completed WAL segments `.done` and reports success WITHOUT uploading.
   Segments completing during the enablement window — including the first
   backup's `begin_wal` — are silently swallowed. Postgres archives strictly
   in order, so nothing looks wrong until a restore fails with
   `WAL not found`.

The sequence is therefore always:

```sh
# 1. Merge the app CR flags + Plan; wait for Flux.
# 2. First ad-hoc BackupJob (activates archiving; its artifact is suspect):
kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata: {name: <app>-initial, namespace: <ns>}
spec:
  applicationRef: {apiGroup: apps.cozystack.io, kind: Postgres, name: <app>}
  backupClassName: cozy-default
EOF
# 3. Second ad-hoc BackupJob after #1 succeeds — its WAL range lies entirely
#    inside the real-archiving era; this is the first restorable backup.
# 4. If the database is idle, force the end_wal segment out:
#    kubectl exec <primary> -c postgres -- psql -U postgres -c "SELECT pg_switch_wal();"
# 5. Verify begin_wal..end_wal objects exist in R2 before trusting it
#    (compare backup.info begin_wal/end_wal against wals/ listing).
```

## Restore drill (to-copy, non-destructive)

Create an empty scratch Postgres app and point a RestoreJob at it; the
source keeps running untouched. For the example below, use a BackupJob from
`tenant-root/verself-controlplane`. Write a marker row on the source AFTER
the base backup and force a WAL switch first — the marker can then only
appear in the restored copy via WAL replay, which proves PITR rather than
just base-backup restore.

```sh
kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Postgres
metadata: {name: verself-controlplane-drill, namespace: tenant-root}
spec: {replicas: 1, size: 10Gi, storageClass: replicated, external: false, version: v18}
---
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata: {name: verself-controlplane-drill-restore, namespace: tenant-root}
spec:
  backupRef: {name: <BackupJob name>}
  targetApplicationRef: {apiGroup: apps.cozystack.io, kind: Postgres, name: verself-controlplane-drill}
EOF
# watch: kubectl -n tenant-root get restorejob <name> -o jsonpath='{.status.phase}'
# verify marker: kubectl exec postgres-verself-controlplane-drill-1 -c postgres -- \
#   psql -U postgres -c "SELECT * FROM restore_drill_marker ORDER BY id;"
# teardown: delete the RestoreJob and the scratch Postgres app CR.
```

`spec.options.recoveryTime` (RFC3339) on the RestoreJob selects a
point-in-time instead of latest. Omitting `targetApplicationRef` makes the
restore IN-PLACE and destructive — never do that in a drill.

## Known limits (stated, not hidden)

- **Idle-database RPO**: bounded at ~5 minutes. Every Guardian Postgres CR
  stamps `archive_timeout: 300s` through the chart's
  `postgresql.parameters` surface, so an idle database force-switches its
  WAL segment and archives it within 5 minutes; active databases archive
  within ~1s of segment completion. Mostly-empty forced segments compress
  to near nothing under barman's gzip.
- The first BackupJob per instance is a WAL-activation artifact, not a
  restore point (see the two-BackupJob rule above). Its base objects age out
  via barman's 30d retention.
- The instance name `guardian` is retired. Its barman prefix remains in the
  `guardian-backups` bucket until retention ages it out; the bucket's 7-day
  Age lock prevents immediate cleanup. CNPG refuses WAL archiving into a
  non-empty destination, so the name must not be reused for a new tenant-root
  Postgres with `useSystemBucket` until the prefix is gone.
- Scheduled/automated drills with recorded RPO/RTO metrics are a follow-up;
  this runbook is the manual procedure they will automate.

# Backup Audit And Restore Drill

How Guardian verifies its backups are secure, bounded in size, and
actually restorable. The industry framing (what DR-focused shops converge
on) in one paragraph: **a backup does not exist until it has been
restored**; verification is *scheduled*, not ad-hoc; every drill leaves a
recorded artifact (that record IS the SOC2 evidence — availability
criteria audit the *management* of backups: documented procedure,
recurring execution, tested restores, measured RPO/RTO); and the checks
run against the storage system directly, never only against the tool that
wrote the backups (the writer lying to you is the failure mode).

What this covers: the `guardian-backups` R2 bucket fed by Cozystack's
`cozy-default` BackupClass — CNPG/barman for
`tenant-root/postflight-controlplane` and `tenant-guardian-prod/keycloak`
Postgres (continuous WAL + nightly base), clickhouse-backup for
`tenant-root/analytics` (nightly archive), and talos-backup for
age-encrypted etcd snapshots every six hours (`talos-etcd/` prefix).
Sibling procedure docs: `postgres-backup-restore.md` (enablement,
two-BackupJob rule, drill mechanics), `analytics-clickhouse.md` (CH
specifics + chart bugs), `etcd-snapshot-restore.md` (etcd recovery
ladder + drill log).

## Cadence

| Check | Cadence | Section |
|---|---|---|
| Backup freshness (did last night's Plans run?) | Weekly, and after any platform change | 1 |
| Security probes (token scope, bucket lock, credential residency) | Monthly, and after any credential/bucket change | 2 |
| Size & retention audit | Monthly | 3 |
| Restore drill (both engines, marker-row PITR for PG) | Monthly while cheap; quarterly floor once automated | 4 |
| Full DR exercise (cold-boot + custody re-seed + restores) | Per the DR program (see static-seal runbook) | — |

Automation follow-up (deliberate, not yet built): each check below should
eventually push its numbers to VictoriaMetrics as a side effect (the
loadtesting practice: gates RECORD throughput/latency, not just pass/fail)
with an alert on `drill-not-passed-in-N-days`. Until then this is a
manual, copy-paste-executable procedure — that is acceptable; unrecorded
drills are not.

## 1. Freshness

Every Plan should have produced a Backup within its period. Judge from
BOTH sides — the CR record and the object store:

```sh
kubectl get backups.backups.cozystack.io -A --sort-by=.metadata.creationTimestamp
# and per-instance WAL liveness (the real RPO signal for Postgres):
kubectl exec -n tenant-root postgres-postflight-controlplane-<primary> -c postgres -- \
  psql -U postgres -tc "SELECT last_archived_wal, last_archived_time, failed_count FROM pg_stat_archiver;"
```

Red flags: no Backup younger than 25h for any covered instance;
`failed_count` growing; `last_archived_time` older than ~1h on an active
database. Remember the idle-database caveat: no `archive_timeout` means an
idle instance's current segment sits local until something fills it — for
truly idle databases the nightly base bounds RPO, and that is a recorded,
accepted limit.

## 2. Security

Run the probes with the bucket-scoped keypair from custody
(`cloudflare_r2_backups_*`), against R2 directly:

```sh
# expected: list_buckets DENIED; guardian-vault access DENIED;
# write+read INSIDE guardian-backups OK; delete of a <7d object REFUSED
# with ObjectLockedByBucketPolicy. The probe object under probe/ is the
# canary — its undeletability IS the lock verification.
python3 - <<'EOF'
import boto3, os, botocore
s3 = boto3.client("s3", endpoint_url=os.environ["cloudflare_r2_s3_api_endpoint"],
    aws_access_key_id=os.environ["cloudflare_r2_backups_access_key_id"],
    aws_secret_access_key=os.environ["cloudflare_r2_backups_secret_access_key"],
    region_name="auto")
def expect_denied(fn, label):
    try: fn(); print(f"FAIL: {label} was ALLOWED")
    except botocore.exceptions.ClientError as e: print(f"ok: {label} denied ({e.response['Error']['Code']})")
expect_denied(lambda: s3.list_buckets(), "account bucket listing")
expect_denied(lambda: s3.list_objects_v2(Bucket="guardian-vault", MaxKeys=1), "cross-bucket read")
s3.put_object(Bucket="guardian-backups", Key="probe/audit", Body=b"ok")
expect_denied(lambda: s3.delete_object(Bucket="guardian-backups", Key="probe/audit"), "delete under bucket lock")
EOF
```

Then the non-scriptable half:

- **Lock rule present and sane**: `GET /accounts/<id>/r2/buckets/guardian-backups/lock`
  shows the Age rule, enabled, and its window is SHORTER than barman's
  `retentionPolicy` (30d) — a lock ≥ retention silently breaks pruning.
- **Credential residency**: the keypair exists in exactly three places —
  custody env, OpenBao `tenant-root/backups-r2`, and the ESO-materialized
  `guardian-backups-creds` (+ the platform-projected `cozy-backups-creds`
  copies). Nothing in Git (`git grep` the access key id), nothing in CI.
- **Token inventory** (dashboard): one bucket-scoped Object R/W token for
  `guardian-backups`; the account-scoped operator token exists but is
  custody-only — it must never appear in any in-cluster Secret.
- **Backup contents assumption check**: prod Keycloak carries no live
  credential in-database (file vault, PR #401); if a new database joins
  the backup set, re-ask what secret material its dumps would carry.

## 3. Size & retention

```sh
# per-instance object count + bytes, from R2 directly
python3 - <<'EOF'
import boto3, os
from collections import defaultdict
s3 = boto3.client("s3", endpoint_url=os.environ["cloudflare_r2_s3_api_endpoint"],
    aws_access_key_id=os.environ["cloudflare_r2_backups_access_key_id"],
    aws_secret_access_key=os.environ["cloudflare_r2_backups_secret_access_key"],
    region_name="auto")
agg = defaultdict(lambda: [0, 0, None, None])
for page in s3.get_paginator("list_objects_v2").paginate(Bucket="guardian-backups"):
    for o in page.get("Contents", []):
        k = "/".join(o["Key"].split("/")[:2]); a = agg[k]
        a[0] += 1; a[1] += o["Size"]
        a[2] = min(a[2] or o["LastModified"], o["LastModified"])
        a[3] = max(a[3] or o["LastModified"], o["LastModified"])
for k, (n, sz, old, new) in sorted(agg.items()):
    print(f"{k}: {n} objects {sz/2**20:.1f} MiB oldest={old:%Y-%m-%d} newest={new:%Y-%m-%d}")
EOF
```

Judge against these invariants (record the numbers each run — the trend
is the alarm, not the absolute):

- **Oldest object age ≤ retention + slack** (~35d for the 30d barman
  policy). Older means pruning is broken — first suspects: bucket lock
  window grew past retention, or a strategy stopped running its
  retention pass. clickhouse-backup accumulates archives per its own
  config; if the CH prefix only ever grows, set/verify its
  `BACKUPS_TO_KEEP_REMOTE` behavior before it balloons.
- **Base backup size trend tracks database size trend.** A base backup
  that jumps far above the live database points at snapshot pollution
  (the bench-table incident: a 1.6G local backup of lab tables from a
  31KiB database). Compare against
  `SELECT pg_database_size(...)` / CH `system.parts` totals.
- **WAL volume is proportional to write activity.** A WAL prefix growing
  fast on an idle database means something is churning (autovacuum
  storms, a runaway writer) — that is a database finding, surfaced by the
  backup audit.
- **No foreign prefixes.** Only `tenant-root/postflight-controlplane/`,
  `tenant-guardian-prod/keycloak/`, `tenant-root/analytics/`, `velero/`
  (when VMs exist), and `probe/` belong in this bucket.

## 4. Restore drill (usability)

The drill IS the audit for usability — nothing else counts. Single-replica
scratch apps keep it cheap; the pattern is non-destructive (to-copy) and
was first executed 2026-07-05.

**Postgres (with PITR proof)** — full mechanics in
`postgres-backup-restore.md`:

1. Write a marker row on the source primary; `SELECT pg_switch_wal()`;
   confirm the segment archived (`pg_stat_archiver`).
2. Scratch 1-replica `Postgres` app + `RestoreJob` with
   `targetApplicationRef` (and optionally `spec.options.recoveryTime` to
   also exercise point-in-time selection, not just latest).
3. Pass = the marker is present in the restored copy. It postdates the
   base backup, so it can only have arrived via WAL replay from R2 —
   that proves PITR, not merely base-restore.
4. Record: RestoreJob apply→Succeeded seconds (RTO component), cluster
   healthy seconds, marker timestamp vs restore time (RPO evidence),
   backup size. Then tear down the scratch app AND its PVs.

**ClickHouse**: scratch 1-replica `ClickHouse` app + `RestoreJob`
targeting it from the latest Backup; pass = row counts of
`guardian_analytics.events` / `otel_traces` match the source at backup
time. Fire only against a stable CHI (`Completed`, sidecars `ready`) and
clear stale local backups first — see the operational lessons in
`analytics-clickhouse.md`.

Drill hygiene, learned live:

- Verify the WAL chain before trusting any Postgres backup:
  `begin_wal`..`end_wal` from `backup.info` must all exist under `wals/`
  (the first-backup WAL hole is silent until restore).
- A failed drill is a FINDING, not a retry-until-green: the 2026-07-05
  drill failing on `WAL not found` is what surfaced the two-BackupJob
  rule. Diagnose before re-firing.
- Never leave drill artifacts: RestoreJob, scratch app, PVs, and (for CH)
  local backup residue on the replicas.

## Drill log

Append one row per drill/audit. This table is the SOC2 evidence trail.

| Date | Type | Target | Result | RTO (apply→healthy) | RPO evidence | Size | Notes |
|---|---|---|---|---|---|---|---|
| 2026-07-05 | Restore drill (PITR) | tenant-root/guardian PG | PASS | 68s to Succeeded, ~2min healthy | marker via WAL replay, archived ≤2s after switch | 4.1 MiB base | First drill; surfaced first-backup WAL hole → two-BackupJob rule |
| 2026-07-05 | Restore drill (PITR) | tenant-root/guardian PG | PASS | 71s to Succeeded, ~85s healthy | marker id=3 @23:41:34 (2h08m post-base) recovered via WAL seg ...00E; archived ~2s after switch | 4.25 MiB base (guardian-second) | 2nd drill, to-copy; re-confirmed WAL-hole in guardian-initial (begin_wal ...008 absent) vs full chain in guardian-second; artifacts + PV torn down clean |
| 2026-07-07 | Restore drill (PITR) | tenant-guardian-prod/products PG | PASS | Succeeded on clean rerun; first run blocked ~40min | post-second-backup marker recovered via WAL replay | fresh instance | Surfaced WAL-hole v2: activation-window segment (begin_wal of the SECOND backup) silently unarchived with pg_stat_archiver showing zero failures; remediated live with barman-cloud-wal-archive from the primary's pg_wal; runbook step 5 verify is mandatory |
| 2026-07-05 | Backup verify | tenant-root/analytics CH | PASS (attempt 4) | — | nightly archive | 105 KiB | Surfaced sidecar OOM on stale local backups + service-recreate abort |
| 2026-07-05 | Security probes | guardian-backups bucket | PASS | — | — | — | Scope denials + lock delete-refusal verified at bucket creation |

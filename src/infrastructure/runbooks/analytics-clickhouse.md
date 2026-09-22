# Analytics ClickHouse (Cozystack tenant app)

The analytics/observability ClickHouse runs as the Cozystack tenant app
`analytics` in `tenant-root` (release `clickhouse-analytics`), migrated
2026-07-05 from a raw Altinity CHI in `guardian-analytics`. Consumers
(ingest, OTel collector, DDL job) stay in `guardian-analytics` and cross
the namespace boundary under the Cilium allowlists in
`deployments/analytics/system/clickhouse.yaml`. Schema/DDL remain repo-owned
(`deployments/analytics/system/*-configmap.yaml`, cluster name
`clickhouse`, 2 replicas). Compression re-verified on the chart's 24.9
server: 16.67 B/event vs the 16.71 baseline.

## User credentials (chart-generated, OpenBao-relayed)

Every user declared on the app CR gets a chart-generated password in
`Secret/clickhouse-analytics-credentials` (tenant-root), and every one of
them reaches its consumer through the same kv secret,
`kv/guardian/guardian-mgmt/guardian-analytics/clickhouse` — one property per
user. The relay is a one-time operator step per user, and must be RE-RUN for
ALL of them after any DR rebuild of the app: the chart regenerates every
password when its Secret is absent, and a stale one fails closed at the
first query.

| kv property | consumer Secret (guardian-analytics) | key | consumer |
| --- | --- | --- | --- |
| `ingest` | `analytics-ch-ingest` | `ingest` | ingest service, OTel collector, DDL Job |
| `payments_canary` | `payments-checkout-canary` | `clickhouse_password` | payments checkout canary (readonly) |
| `cli_canary` | `cli-release-canary` | `clickhouse_password` | postflight CLI release canaries |

```sh
# guardian-writer-guardian-analytics flow, value never on argv, per property:
# read tenant-root/clickhouse-analytics-credentials key <property>, write it
# to kv/guardian/guardian-mgmt/guardian-analytics/clickhouse property
# <property> via the static-seal runbook's "Adding An Integration" procedure
# (a kv write REPLACES the secret — put every property in one write), then
# force-sync the consumer ExternalSecrets.
```

A `cli_canary` that has drifted is visible rather than silent:
`GuardianCliDeeptestEventWriteFailing` fires off
`guardian_cli_deeptest_event_write` when the deep-test runner's INSERT is
rejected.

## Backups (guardian-r2 Plan, guardian-r2-altinity strategy)

Nightly `Plan` `analytics-clickhouse-nightly` at 05:00 UTC in tenant-root
drives the chart's clickhouse-backup sidecar on replica 0-0 through the
`guardian-r2` BackupClass's `guardian-r2-altinity` strategy
(`base/backup/clickhouse-backup-strategy.yaml`); archives land in
`guardian-backups` R2 under `tenant-root/analytics/`. The strategy is the
platform's `cozy-default-altinity` plus three changes (the file header has
the verified clickhouse-backup v2.7.4 source references):

- `create_remote` passes `delete_source=true`, so the local copy is deleted
  once the upload succeeds, and each run first deletes every local
  `clickhouse-analytics-*` backup a failed run left behind. Local backups
  never accumulate; restores read from R2.
- The poll loop fails the pod after 2 minutes with the action missing
  from `/backup/actions` or the API unreachable ("vanished ... sidecar
  restart (OOMKilled?)"), and on a `cancel` status. Upstream polled forever.
- `activeDeadlineSeconds: 3600` per pod; the driver's Job has
  backoffLimit 2, and every retry picks a fresh target name.

Never retry a backup under an existing name: the bucket's object-lock
policy rejects the overwrite (`ObjectLockedByBucketPolicy`), and
clickhouse-backup refuses a name that already exists on remote unless
`resume` is passed, which this strategy never does. Re-diff the strategy
against `kubectl get altinity cozy-default-altinity -o yaml` on every
Cozystack upgrade.

Alerts (`deployments/alerting/clickhouse-backup-alerts.yaml`, all from
kube-state-metrics Job series; Cozystack exports no BackupJob metrics):

| Alert | Fires when | First move |
| --- | --- | --- |
| `ClickHouseBackupStale` (critical) | newest succeeded nightly Job completed > 26h ago | read the latest BackupJob's Job logs, then the sidecar's |
| `ClickHouseBackupStuck` (warning) | a nightly Job active > 2h | check the sidecar for OOMKills and local backups (below) |
| `ClickHouseBackupMetricsAbsent` (critical) | no Job owned by a nightly BackupJob in KSM | Plan deleted or KSM blind; the other two are dark |
| `KubeJobFailed` (warning, platform) | the Plan's latest run failed | as Stale; older failures are ignored once a newer run succeeds |

The sidecar
reads bucket coordinates from `Secret/guardian-backups-creds` directly
(`backup.s3CredentialsSecret`), NOT via `useSystemBucket` — see chart bug
2 below. Restores go through `backups.cozystack.io/RestoreJob` like
Postgres (see runbooks/postgres-backup-restore.md for the drill pattern).

## Cozystack chart bugs (hit live; drop workarounds when fixed)

1. **Keeper DNS**: the CHI template's zookeeper block reads
   `.Values.clusterDomain` (undefined; everything else uses the injected
   `_cluster` value), rendering keeper hosts as absolute FQDNs ending in
   `.svc.` that never resolve — every Replicated* engine on a chart CH app
   fails with KEEPER_EXCEPTION. Workaround: declare `clusterDomain:
   cozy.local` on the app CR (schema allows extra fields; it feeds exactly
   the variable the template reads).
2. **useSystemBucket vs https S3**: the platform credentials projector
   scheme-strips the projected `endpoint` key, and clickhouse-backup's AWS
   SDK rejects schemeless URIs ("was not a valid URI"). Workaround: point
   `backup.s3CredentialsSecret` at `guardian-backups-creds` (full https
   endpoint; key names match chart defaults, including `region`) with
   `s3PathOverride: tenant-root/analytics` pinned to the prefix the
   platform flow would have used — flipping back later is values-only.
3. **storageClass is unwired**: the value exists in the schema but no
   template consumes it; data PVCs land on the cluster default (DRBD
   `replicated`). Accepted at current volume (6x raw at 2 replicas);
   revisit at scale or when upstream wires it.
4. **Service-type recreate abort**: the chart's serviceTemplate omits
   `type`, so the first post-install spec change makes the operator try to
   recreate `chendpoint-<release>` ("service type change 'ClusterIP'=>''")
   and the whole CHI reconcile can land in `Aborted`. Recovery: delete the
   chendpoint Service and bump `spec.taskID` on the CHI to force a
   reconcile — it recreates cleanly.

## Operational lessons (hit live 2026-07-05)

- **Local backups OOM the sidecar.** The sidecar has chart-fixed 256Mi
  limits; local backups under `/var/lib/clickhouse/backup/` (hardlinks —
  REAL disk once source parts are dropped) push subsequent operations
  over the limit → OOMKilled crashloop → strategy Jobs fail with "Could
  not connect ... port 7171". Until 2026-09 the upstream strategy never
  deleted local copies, even after successful uploads: replica 0-0 held 73
  (07-17 → 09-22), and the 09-16 and 09-19 nightlies were OOMKilled
  seconds into `create_remote` and then hung for days on the lost action.
  `guardian-r2-altinity` now deletes them itself. If the sidecar is
  crashlooping (the strategy cannot reach it to clean up), clear them by
  hand on every replica before retrying:
  `kubectl -n tenant-root exec chi-clickhouse-analytics-clickhouse-0-0-0 -c clickhouse -- sh -c 'rm -rf /var/lib/clickhouse/backup/*'`
  (repeat for `-0-1-0`). Never run benches/bulk loads while a backup
  could snapshot them.
- `restore_remote` downloads into the same local directory and nothing
  deletes that copy; clear it the same way after a restore drill.
- BackupJobs racing a CHI rollout fail on unreachable per-host sidecars;
  wait for CHI `Completed` AND sidecar `ready=true` before firing.
- A partial remote archive from a failed attempt stays in R2 (bucket lock
  blocks deletion for 7 days); clean it after lock expiry or let it age
  out of retention.

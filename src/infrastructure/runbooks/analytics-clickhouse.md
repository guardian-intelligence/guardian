# Analytics ClickHouse (Cozystack tenant app)

The analytics/observability ClickHouse runs as the Cozystack tenant app
`analytics` in `tenant-root` (release `clickhouse-analytics`), migrated
2026-07-05 from a raw Altinity CHI in `guardian-analytics`. Consumers
(ingest, OTel collector, DDL job) stay in `guardian-analytics` and cross
the namespace boundary under the Cilium allowlists in
`base/apps/analytics-clickhouse.yaml`. Schema/DDL remain repo-owned
(`deployments/analytics/system/*-configmap.yaml`, cluster name
`clickhouse`, 2 replicas). Compression re-verified on the chart's 24.9
server: 16.67 B/event vs the 16.71 baseline (docs/analytics-storage-design.md).

## Ingest credential (chart-generated, OpenBao-relayed)

The chart generates the `ingest` user's password into
`Secret/clickhouse-analytics-credentials` (tenant-root). Consumers read
`Secret/analytics-ch-ingest` (guardian-analytics), materialized by ESO from
`kv/guardian/guardian-mgmt/guardian-analytics/clickhouse`. The relay is a
one-time operator step (and must be RE-RUN after any DR rebuild — the chart
regenerates a different password when its Secret is absent):

```sh
# guardian-writer-guardian-analytics flow, value never on argv:
# read tenant-root/clickhouse-analytics-credentials key `ingest`, write it
# to kv/guardian/guardian-mgmt/guardian-analytics/clickhouse property
# `ingest` via the static-seal runbook's "Adding An Integration" procedure,
# then force-sync ExternalSecret analytics-ch-ingest.
```

## Backups (cozy-default Plan, Altinity strategy)

Nightly `Plan` at 05:00 UTC in tenant-root drives the chart's
clickhouse-backup sidecar via the platform's Altinity strategy; archives
land in `guardian-backups` R2 under `tenant-root/analytics/`. The sidecar
reads bucket coordinates from `Secret/guardian-backups-creds` directly
(`backup.s3CredentialsSecret`), NOT via `useSystemBucket` — see chart bug
2 below. Restores go through `backups.cozystack.io/RestoreJob` like
Postgres (see runbooks/postgres-backup-restore.md for the drill pattern).

## Cozystack v1.5.0 chart bugs (both hit live; drop workarounds when fixed)

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

- **Failed create_remote leaves local backups that OOM the sidecar.** The
  sidecar has chart-fixed 256Mi limits; stale local backups under
  `/var/lib/clickhouse/backup/` (hardlinks — REAL disk once source parts
  are dropped) push subsequent operations over the limit → OOMKilled
  crashloop → strategy Jobs fail with "Could not connect ... port 7171".
  After any failed BackupJob: `rm -rf /var/lib/clickhouse/backup/*` on
  every replica (exec via the clickhouse container) before retrying, and
  never run benches/bulk loads while a backup could snapshot them.
- BackupJobs racing a CHI rollout fail on unreachable per-host sidecars;
  wait for CHI `Completed` AND sidecar `ready=true` before firing.
- A partial remote archive from a failed attempt stays in R2 (bucket lock
  blocks deletion for 7 days); clean it after lock expiry or let it age
  out of retention.

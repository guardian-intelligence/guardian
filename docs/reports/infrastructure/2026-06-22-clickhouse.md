# ClickHouse Operational Report

## Scope

- Component: Cozystack ClickHouse.
- Desired state source: `src/infrastructure/base/apps/clickhouse.yaml`,
  `src/infrastructure/base/backups/managed-databases.yaml`,
  `src/infrastructure/evidence/database-dr.yaml`.
- Cluster: `guardian-mgmt`.
- Environment or tenant: `tenant-root`.
- Report date: 2026-06-22.
- Status: pending live execution.

## Preflight

- Render/build validation: `aspect infra preflight` and
  `aspect infra evidence-render`.
- Reconciled Kubernetes resources: `ClickHouse/tenant-root/ledger`,
  `BackupClass/guardian-clickhouse-r2`, `Plan/tenant-root/guardian-clickhouse-hourly`.
- Healthy baseline command: `aspect infra live-snapshot --kubeconfig "${KUBECONFIG}"`.
- Result: pending.

## Load Test

- Command: write/query wide-event rows through the managed ClickHouse endpoint.
- Inputs: test table `default.guardian_evidence_wide_events`.
- Pass criteria: rows read back, Keeper has three replicas, and ClickHouse pods
  stay Ready.
- Result: pending.

## Disaster Recovery Drill

- Command: `aspect infra evidence-apply`, `aspect infra evidence-wait`,
  `aspect infra evidence-restore-apply`, `aspect infra evidence-restore-wait`.
- Restore source: `BackupJob/tenant-root/evidence-clickhouse-adhoc`.
- Restore target: `ClickHouse/tenant-root/ledger-restore-check`.
- Pass criteria: BackupJob and RestoreJob reach `Succeeded`, restored copy
  starts, and validation query returns the pre-backup rows.
- Result: pending.

## Single-Node Outage Exercise

- Procedure: run ClickHouse query loop during one-node outage and check
  `aspect infra live-snapshot`.
- Expected behavior: Keeper remains available and query path recovers without
  manual object repair.
- Result: pending.

## Residual Risk

- Requires `aspect infra seed-db-backup-secret` to create
  `Secret/tenant-root/guardian-r2-db-backups` before backup sidecars can
  complete.

# CNPG / Postgres Operational Report

## Scope

- Component: Cozystack Postgres / CNPG.
- Desired state source: `src/infrastructure/base/apps/postgres.yaml`,
  `src/infrastructure/base/backups/managed-databases.yaml`,
  `src/infrastructure/evidence/database-dr.yaml`.
- Cluster: `guardian-mgmt`.
- Environment or tenant: `tenant-root`.
- Report date: 2026-06-22.
- Status: pending live execution.

## Preflight

- Render/build validation: `aspect infra preflight` and
  `aspect infra evidence-render`.
- Reconciled Kubernetes resources: `Postgres/tenant-root/guardian`,
  `BackupClass/guardian-postgres-r2`, `Plan/tenant-root/guardian-postgres-hourly`.
- Healthy baseline command: `aspect infra live-snapshot --kubeconfig "${KUBECONFIG}"`.
- Result: pending.

## Load Test

- Command: SQL write/read loop through the managed Postgres service, followed by
  `aspect infra live-snapshot`.
- Inputs: test table `guardian_evidence.postgres_load`.
- Pass criteria: all inserted rows read back, CNPG reports three instances, and
  synchronous replica settings remain in effect.
- Result: pending.

## Disaster Recovery Drill

- Command: `aspect infra evidence-apply`, `aspect infra evidence-wait`,
  `aspect infra evidence-restore-apply`, `aspect infra evidence-restore-wait`.
- Restore source: `BackupJob/tenant-root/evidence-postgres-adhoc`.
- Restore target: `Postgres/tenant-root/guardian-restore-check`.
- Pass criteria: BackupJob and RestoreJob reach `Succeeded`, restored copy
  starts, and validation query returns the pre-backup rows.
- Result: pending.

## Single-Node Outage Exercise

- Procedure: run SQL read/write loop during one-node outage and check
  `aspect infra live-snapshot`.
- Expected behavior: writes continue with quorum settings; no manual failover
  repair is needed.
- Result: pending.

## Residual Risk

- Requires `aspect infra seed-db-backup-secret` to create
  `Secret/tenant-root/guardian-r2-db-backups` before reconciliation can
  complete.

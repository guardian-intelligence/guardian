# Managed database backup substrate status

Date: 2026-06-22

## Scope

Components:

- CNPG / Postgres `tenant-root/guardian`.
- ClickHouse `tenant-root/ledger`.

Desired state sources:

- `src/infrastructure/base/apps/postgres.yaml`.
- `src/infrastructure/base/apps/clickhouse.yaml`.
- `src/infrastructure/base/backups/managed-databases.yaml`.
- `src/infrastructure/inventory/guardian-mgmt.json`.

## Declared State

Postgres:

- chart-side backup plumbing is enabled so CNPG WAL archiving is configured from
  install time;
- R2 destination prefix is `s3://guardian-vault/gm/pg/guardian/`;
- hourly `Plan` `tenant-root/guardian-postgres-hourly` references
  `BackupClass/guardian-postgres-r2`.

ClickHouse:

- chart-side backup integration is enabled so the Altinity backup sidecar is
  rendered;
- R2 prefix override is `gm/ch/ledger`;
- hourly `Plan` `tenant-root/guardian-clickhouse-hourly` references
  `BackupClass/guardian-clickhouse-r2`;
- the Altinity strategy uses a digest-pinned curl image and does not install
  packages at runtime.

Shared Secret contract:

- namespace/name: `tenant-root/guardian-r2-db-backups`;
- required keys: `bucketName`, `endpoint`, `region`, `AWS_ACCESS_KEY_ID`,
  `AWS_SECRET_ACCESS_KEY`;
- secret values are not stored in git.

## Current Evidence

- Desired state renders locally with `aspect infra render-base`.
- The backup Secret is checked by name in `aspect infra live-snapshot`, without
  printing secret data.

## Not Yet Passed

- No live `BackupJob` has reached `Succeeded`.
- No `Backup` artifact has been restored to a copy.
- No report proves restored Postgres rows or ClickHouse wide-event rows.
- Secret projection from OpenBao remains a known bootstrap gap; secret-zero must
  seed `tenant-root/guardian-r2-db-backups` before managed database
  reconciliation can complete.

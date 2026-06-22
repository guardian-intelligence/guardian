# Infrastructure evidence overlay status

Date: 2026-06-22

## Scope

Desired state source: `src/infrastructure/evidence/`.

This report records the repo-owned opt-in test fixtures added for live evidence
collection. It does not claim the live load, backup, restore, or outage drills
have passed.

## Declared Fixtures

- `Job/tenant-root/evidence-postgres-load` runs 4 psql workers through
  `postgres-guardian-rw`, inserts 250 rows per worker into
  `guardian_evidence.postgres_load`, and fails unless all 1,000 rows read back.
- `Job/tenant-root/evidence-clickhouse-load` runs 4 clickhouse-client workers
  through `chendpoint-clickhouse-ledger`, inserts 250 wide-event rows per worker
  into `default.guardian_evidence_wide_events`, and fails unless all 1,000 rows
  read back.
- `Job/tenant-root/evidence-http-load` runs repeated HTTPS checks against the
  prod/dev/gamma company-site routes, Harbor health, and dashboard host.
- `PersistentVolumeClaim/tenant-root/evidence-replicated-retain` plus
  `Job/tenant-root/evidence-storage-smoke` seed and verify checksummed data on
  the default retained replicated storage path.
- `BackupJob/tenant-root/evidence-postgres-adhoc` and
  `RestoreJob/tenant-root/evidence-postgres-to-copy` exercise Postgres
  restore-to-copy into `Postgres/tenant-root/guardian-restore-check`.
- `BackupJob/tenant-root/evidence-clickhouse-adhoc` and
  `RestoreJob/tenant-root/evidence-clickhouse-to-copy` exercise ClickHouse
  restore-to-copy into `ClickHouse/tenant-root/ledger-restore-check`.

## Command Surface

```sh
aspect infra evidence-render
aspect infra evidence-clean --kubeconfig "${KUBECONFIG}"
aspect infra evidence-apply --kubeconfig "${KUBECONFIG}"
aspect infra evidence-wait --kubeconfig "${KUBECONFIG}" --timeout 30m
aspect infra evidence-restore-apply --kubeconfig "${KUBECONFIG}"
aspect infra evidence-restore-wait --kubeconfig "${KUBECONFIG}" --timeout 30m
aspect infra evidence-logs --kubeconfig "${KUBECONFIG}"
aspect infra evidence-snapshot --kubeconfig "${KUBECONFIG}"
```

## Current Evidence

- The overlay renders locally with the repo-pinned kubectl.
- The main evidence overlay renders 13 Kubernetes documents; the deferred
  restore manifest renders 2 additional `RestoreJob` documents.
- `aspect infra evidence-clean` is declared so the evidence loop can be rerun
  without manual Kubernetes deletion.
- The pinned curl, Postgres client, and ClickHouse client images used by the
  Jobs are digest-addressed.

## Not Yet Passed

- No live evidence Job has completed in `guardian-mgmt`.
- No live `BackupJob` or `RestoreJob` has reached `Succeeded`.
- No component report has consumed the overlay output yet.

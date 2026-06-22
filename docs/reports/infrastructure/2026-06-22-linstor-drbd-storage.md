# LINSTOR / DRBD Storage Operational Report

## Scope

- Component: LINSTOR, DRBD, replicated storage classes.
- Desired state source: `src/infrastructure/base/storage/`,
  `src/infrastructure/evidence/storage-smoke.yaml`.
- Cluster: `guardian-mgmt`.
- Environment or tenant: `tenant-root`.
- Report date: 2026-06-22.
- Status: pending live execution.

## Preflight

- Render/build validation: `aspect infra preflight` and
  `aspect infra evidence-render`.
- Reconciled Kubernetes resources: `StorageClass/replicated`,
  `StorageClass/replicated-retain`, `LinstorSatelliteConfiguration/guardian-data-pool`.
- Healthy baseline command: `aspect infra live-snapshot --kubeconfig "${KUBECONFIG}"`.
- Result: pending.

## Load Test

- Command: `aspect infra evidence-apply`, `aspect infra evidence-wait`, then
  `aspect infra evidence-logs`.
- Inputs: `PVC/tenant-root/evidence-replicated-retain`.
- Pass criteria: `Job/evidence-storage-smoke` completes with zero checksum
  failures and repeat execution verifies the retained checksum manifest.
- Result: pending.

## Disaster Recovery Drill

- Failure injected: delete storage smoke Pod and reschedule; later, power off
  one node holding a replica.
- Restore source: three-way DRBD replicas from `replicated-retain`.
- Pass criteria: checksum verification succeeds after reschedule and during
  one-node loss; no manual LINSTOR repair is required.
- Result: pending.

## Single-Node Outage Exercise

- Procedure: run storage smoke, power off one node, rerun storage smoke and
  `aspect infra live-snapshot`, then restore the node.
- Expected behavior: replicated volumes stay writable for healthy workloads.
- Result: pending.

## Residual Risk

- Live DRBD placement must be confirmed with `linstor resource list` after
  convergence.

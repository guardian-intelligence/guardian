# Prod / Root Tenant Operational Report

## Scope

- Component: Cozystack root tenant serving prod.
- Desired state source: `src/infrastructure/base/tenants/root.yaml`,
  `src/environments/prod/environment.yaml`.
- Cluster: `guardian-mgmt`.
- Environment or tenant: `tenant-root`.
- Report date: 2026-06-22.
- Status: pending live execution.

## Preflight

- Render/build validation: `aspect infra preflight`.
- Reconciled Kubernetes resources: `Tenant/tenant-root/root`, namespace
  `tenant-root`, root ingress, monitoring, etcd, and seaweedfs tenant services.
- Healthy baseline command: `aspect infra live-snapshot --kubeconfig "${KUBECONFIG}"`.
- Result: pending.

## Load Test

- Command: `aspect infra evidence-apply`, `aspect infra evidence-wait`, then
  `aspect infra evidence-logs`.
- Inputs: prod/root tenant public routes and shared tenant services.
- Pass criteria: prod company-site, Harbor health, dashboard route, and
  tenant-root workloads stay Ready.
- Result: pending.

## Disaster Recovery Drill

- Failure injected: delete one root-tenant workload Pod per service class and
  verify reconciliation.
- Restore source: base manifests, replicated storage, and app backup plans.
- Pass criteria: root tenant returns to desired workloads without manual
  namespace repair.
- Result: pending.

## Single-Node Outage Exercise

- Procedure: run `aspect infra live-snapshot`, HTTP evidence, and database
  evidence during one-node outage.
- Expected behavior: root tenant services continue or recover within their
  rollout timeouts.
- Result: pending.

## Residual Risk

- Root tenant hosts most shared services, so final evidence depends on Harbor,
  Postgres, ClickHouse, OpenBao, and storage drills passing.

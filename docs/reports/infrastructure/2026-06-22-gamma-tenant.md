# Gamma Tenant Operational Report

## Scope

- Component: Cozystack gamma tenant.
- Desired state source: `src/infrastructure/base/tenants/environments.yaml`,
  `src/environments/gamma/environment.yaml`.
- Cluster: `guardian-mgmt`.
- Environment or tenant: `tenant-gamma`.
- Report date: 2026-06-22.
- Status: pending live execution.

## Preflight

- Render/build validation: `aspect infra preflight`.
- Reconciled Kubernetes resources: `Tenant/tenant-root/gamma`, namespace
  `tenant-gamma`, company-site Deployment/Service/Ingress.
- Healthy baseline command: `aspect infra live-snapshot --kubeconfig "${KUBECONFIG}"`.
- Result: pending.

## Load Test

- Command: `aspect infra evidence-apply`, `aspect infra evidence-wait`, then
  `aspect infra evidence-logs`.
- Inputs: `https://gamma.guardianintelligence.org/`.
- Pass criteria: gamma site routes return success and gamma tenant workloads
  stay Ready.
- Result: pending.

## Disaster Recovery Drill

- Failure injected: delete gamma company-site Pod and verify tenant
  reconciliation.
- Restore source: base manifests plus Harbor-published company-site digest.
- Pass criteria: gamma deployment returns to desired replicas without manual
  namespace repair.
- Result: pending.

## Single-Node Outage Exercise

- Procedure: run HTTP evidence during each one-node outage.
- Expected behavior: gamma route remains reachable or recovers within rollout
  timeout.
- Result: pending.

## Residual Risk

- Gamma site pull depends on Harbor publication.

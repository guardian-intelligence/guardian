# Dev Tenant Operational Report

## Scope

- Component: Cozystack dev tenant.
- Desired state source: `src/infrastructure/base/tenants/environments.yaml`,
  `src/environments/dev/environment.yaml`.
- Cluster: `guardian-mgmt`.
- Environment or tenant: `tenant-dev`.
- Report date: 2026-06-22.
- Status: pending live execution.

## Preflight

- Render/build validation: `aspect infra preflight`.
- Reconciled Kubernetes resources: `Tenant/tenant-root/dev`, namespace
  `tenant-dev`, company-site Deployment/Service/Ingress.
- Healthy baseline command: `aspect infra live-snapshot --kubeconfig "${KUBECONFIG}"`.
- Result: pending.

## Load Test

- Command: `aspect infra evidence-apply`, `aspect infra evidence-wait`, then
  `aspect infra evidence-logs`.
- Inputs: `https://dev.guardianintelligence.org/`.
- Verifier check: `company-site:dev:ready`.
- Pass criteria: dev site routes return success and dev tenant workloads stay
  Ready.
- Result: pending.

## Disaster Recovery Drill

- Failure injected: delete dev company-site Pod and verify tenant reconciliation.
- Restore source: base manifests plus Harbor-published company-site digest.
- Pass criteria: dev deployment returns to desired replicas without manual
  namespace repair.
- Result: pending.

## Single-Node Outage Exercise

- Procedure: run HTTP evidence during each one-node outage.
- Verifier check: `company-site:dev:ready` in each outage phase.
- Expected behavior: dev route remains reachable or recovers within rollout
  timeout.
- Result: pending.

## Residual Risk

- Dev site pull depends on Harbor publication.

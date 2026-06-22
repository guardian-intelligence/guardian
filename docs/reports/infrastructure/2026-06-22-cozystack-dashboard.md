# Cozystack Dashboard Operational Report

## Scope

- Component: Cozystack Dashboard.
- Desired state source: `src/infrastructure/base/cozystack/platform.yaml`,
  `src/infrastructure/evidence/http-load.yaml`.
- Cluster: `guardian-mgmt`.
- Environment or tenant: management root tenant.
- Report date: 2026-06-22.
- Status: pending live execution.

## Preflight

- Render/build validation: `aspect infra preflight` and
  `aspect infra evidence-render`.
- Reconciled Kubernetes resources: `Package/cozystack.cozystack-platform`.
- Healthy baseline command: `aspect infra live-snapshot --kubeconfig "${KUBECONFIG}"`.
- Result: pending.

## Load Test

- Command: `aspect infra evidence-apply`, `aspect infra evidence-wait`, then
  `aspect infra evidence-logs`.
- Inputs: `https://dashboard.guardianintelligence.org/`.
- Verifier check: `load:http:dashboard-root`.
- Pass criteria: HTTP requests succeed or return the expected auth redirect
  without TLS errors.
- Result: pending.

## Disaster Recovery Drill

- Failure injected: restart dashboard/ingress pods and verify route recovers.
- Restore source: platform package reconciliation.
- Pass criteria: dashboard host returns expected response after pod restart and
  one-node outage.
- Result: pending.

## Single-Node Outage Exercise

- Procedure: keep HTTP evidence job running during a single-node outage.
- Expected behavior: dashboard host remains reachable through public ingress.
- Result: pending.

## Residual Risk

- Dashboard auth behavior depends on Keycloak/OIDC convergence.

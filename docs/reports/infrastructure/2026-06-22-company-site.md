# Company Site Dev/Gamma/Prod Operational Report

## Scope

- Component: Guardian Intelligence company website.
- Desired state source: `src/products/company/site/`,
  `src/infrastructure/base/products/company-site.yaml`,
  `src/environments/{dev,gamma,prod}/environment.yaml`,
  `src/infrastructure/evidence/http-load.yaml`.
- Cluster: `guardian-mgmt`.
- Environment or tenant: `tenant-dev`, `tenant-gamma`, `tenant-root`.
- Artifact digest:
  `sha256:708390f2a646b7286fdc29c6d9bc0cc789932aa7ae6fa899ce436084e5435277`.
- Report date: 2026-06-22.
- Status: pending live execution.

## Preflight

- Render/build validation: `aspect infra preflight`,
  `bazelisk build //src/products/company/site:image.digest`.
- Reconciled Kubernetes resources: company-site Deployment, Service, and Ingress
  in dev, gamma, and prod/root tenants.
- Healthy baseline command: `aspect infra live-rollout --kubeconfig "${KUBECONFIG}"`.
- Result: pending.

## Load Test

- Command: `aspect infra evidence-apply`, `aspect infra evidence-wait`, then
  `aspect infra evidence-logs`.
- Inputs: `/`, `/letters/`, `/news/`, `/healthz`, and `/metrics` on prod, dev,
  and gamma hosts.
- Verifier checks:
  `company-site:{dev,gamma,prod}:ready` and
  `load:http:company-{prod,dev,gamma}-{root,letters,news,healthz,metrics}`.
- Pass criteria: HTTP evidence job reports zero failures and all three
  deployments stay Ready.
- Result: pending.

## Disaster Recovery Drill

- Failure injected: delete one company-site Pod per tenant and verify rollout.
- Restore source: Harbor-published immutable digest.
- Pass criteria: pods return to desired replicas and routes keep passing HTTP
  evidence.
- Result: pending.

## Single-Node Outage Exercise

- Procedure: run HTTP evidence during each one-node outage and then
  `aspect infra live-rollout`.
- Expected behavior: all three sites remain reachable or recover within rollout
  timeout.
- Result: pending.

## Residual Risk

- Requires live Harbor publication before Kubernetes can pull the digest.

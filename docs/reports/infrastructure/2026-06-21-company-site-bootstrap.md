# 2026-06-21 Company Site Bootstrap Report

## Scope

- Component: company-site
- Desired state source:
  `src/products/company/site/`,
  `src/infrastructure/base/products/company-site.yaml`,
  `src/environments/{dev,gamma,prod}/environment.yaml`
- Cluster: guardian-mgmt
- Environment or tenant: tenant-dev, tenant-gamma, tenant-root
- Image digest:
  `sha256:708390f2a646b7286fdc29c6d9bc0cc789932aa7ae6fa899ce436084e5435277`

## Preflight

- `bazelisk build //src/products/company/site:image //src/products/company/site:image.digest`
  completed successfully.
- `aspect infra render-base` rendered the company-site Deployments, Services,
  and Ingresses for dev, gamma, and prod.
- `bazelisk run //src/products/company/site:load` loaded the image into Podman
  as `localhost/guardian/company-site:dev`.
- Local HTTP probes against the loaded image passed for `/`, `/healthz`, and
  `/metrics`.
- Harbor publication is declared as `//src/products/company/site:push-harbor`
  and exposed as `aspect infra publish-company-site`; live publication is still
  pending Harbor convergence and OCI auth.

## Load Test

Pending live cluster convergence.

## Disaster Recovery Drill

Pending live cluster convergence.

## Single-Node Outage Exercise

Pending live cluster convergence.

## Residual Risk

- The image has not been pushed into Harbor yet.
- Public DNS has not been applied to route dev/gamma/prod traffic to the
  management cluster.
- Live readiness, TLS issuance, and ingress routing are unverified.

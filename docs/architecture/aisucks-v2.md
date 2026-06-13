# aisucks v2 architecture

Status: scratch-rewrite target, 2026-06-13.

`aisucks` is the first Guardian product surface to move behind a platform API.
The first slice is intentionally tiny: a charter page, `/healthz`, `/livez`,
`/api/v1/hello`, and an npm SDK that calls the hello endpoint.

## Platform Contract

```text
guardian up
  -> bootstrap Talos + Cilium + seed registry
  -> push workspace-built OCI images by digest
  -> render the platform PublicHttpService envelope
  -> converge aisucks as the first product consumer
```

The current implementation direct-renders the `PublicHttpService` envelope from
`src/platform/public-http-service/`. The Crossplane shape lives in
`src/crossplane/configurations/guardian-products/` and becomes live once the
Configuration package and any required functions/providers are pinned and
mirrored.

## APIs

Top-level product API:

```yaml
apiVersion: products.guardian.dev/v1alpha1
kind: AisucksProduct
metadata:
  namespace: aisucks
  name: default
spec:
  domain: aisucks.app
  image: registry.guardian.internal/aisucks@sha256:...
  observability:
    profile: standard
```

Reusable platform API:

```yaml
apiVersion: platform.guardian.dev/v1alpha1
kind: PublicHttpService
metadata:
  namespace: aisucks
  name: aisucks
spec:
  app: aisucks
  domain: aisucks.app
  image: registry.guardian.internal/aisucks@sha256:...
  podNetwork: true
  observability:
    profile: standard
```

`AisucksProduct` composes `PublicHttpService`. `PublicHttpService` owns the
boring service envelope: namespace, deployment, service, TLS custody, Gateway
route shape, probes, metrics scrape identity, and alert profile.

## Boundaries

Crossplane/platform-owned:

```text
AisucksProduct
PublicHttpService
ObservedService profile
future PostgresDatabase
future pipeline envelopes
```

Runtime-owned:

```text
HTTP handlers
SDK contract
product transactions
future verifier jobs
future reviewer decisions
future RL artifacts
```

The useful rule stays unchanged: Crossplane owns durable capability envelopes
that need converge/status/delete semantics. Hot product state stays in the
runtime and its databases.

## Repository Shape

```text
src/products/aisucks/
  web/                    # TanStack source for the page
  services/api/           # Go API, OCI image

src/viteplus-monorepo/packages/aisucks-sdk/
  src/index.ts            # npm SDK

src/platform/public-http-service/
  k8s/public-http-service.yaml.tmpl

src/crossplane/configurations/guardian-products/
  apis/
  compositions/
  examples/
```

## Next Slices

1. Package and install pinned Crossplane + `guardian-products`.
2. Replace the direct renderer with `AisucksProduct` XR apply.
3. Add product state with operator-managed Postgres and SQLC.
4. Add verifier and reviewer workflows behind explicit APIs.
5. Add Electric/TanStack DB read sync only after the write path is explicit.

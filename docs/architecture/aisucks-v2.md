# aisucks v2 architecture

Status: scratch-rewrite target, 2026-06-13.

`aisucks` is the first Guardian product surface to move behind a platform API.
The current runtime surface is intentionally tiny: a charter page, `/healthz`,
and `/livez`. The public SDK contract is moving to Connect/RPC Health before
the runtime exposes product writes.

## Platform Contract

```text
guardian up
  -> bootstrap Talos + Cilium + seed registry
  -> seed OpenBao, Crossplane, provider-kubernetes, Flux, and pinned functions
  -> push bootstrap-required OCI artifacts by digest
  -> hand off to Flux/Crossplane
  -> Crossplane converges AisucksProduct through PublicHttpService
```

The current implementation declares `AisucksProduct` in the site environment
bundle. `AisucksProduct` composes
`platform.guardian.dev/PublicHttpService`, which renders the Kubernetes
workload envelope. The Guardian CLI is not the product deployment API; it only
prepares the host and bootstrap substrate required for the cluster reconcilers
to take over.

## APIs

Top-level product API:

```yaml
apiVersion: products.guardian.dev/v1alpha1
kind: AisucksProduct
metadata:
  name: aisucks
spec:
  site: prod
  domain: aisucks.app
  image: registry.guardian.internal/aisucks@sha256:...
  replicas: 2
```

Reusable platform API:

```yaml
apiVersion: platform.guardian.dev/v1alpha1
kind: PublicHttpService
metadata:
  name: aisucks
spec:
  site: prod
  namespace: aisucks
  app: aisucks
  domain: aisucks.app
  image: registry.guardian.internal/aisucks@sha256:...
  podNetwork: true
  replicas: 2
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
  api/                    # Protobuf contract and operation policy metadata
  web/                    # TanStack source for the page
  services/api/           # Go API, OCI image

src/viteplus-monorepo/packages/aisucks-sdk/
  src/index.ts            # npm SDK wrapper over generated Connect client

src/crossplane/packages/guardian-products/
  aisucks-product.yaml

src/crossplane/packages/guardian-platform/
  public-http-service.yaml
```

## Next Slices

1. Add product state with operator-managed Postgres and SQLC.
2. Add verifier and reviewer workflows behind explicit APIs.
3. Add Electric/TanStack DB read sync only after the write path is explicit.

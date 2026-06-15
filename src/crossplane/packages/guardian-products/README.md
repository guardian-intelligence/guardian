# guardian-products Crossplane shape

Status: product API skeleton, not installed by `guardian up` yet.

This directory defines the product API shape Guardian is moving toward:

- `platform.guardian.dev/PublicHttpService` is the reusable envelope for a
  public HTTP workload: namespace, deployment, service, TLS custody, probes,
  metrics scrape identity, and alert profile.
- `products.guardian.dev/AisucksProduct` is a thin product declaration that
  composes one `PublicHttpService` in this first product slice.

The live product implementation for this iteration is still the repo-owned
platform template at `src/platform/public-http-service/`. The platform
EdgeGateway substrate is now installed separately by `guardian up`; moving
`PublicHttpService` itself into Crossplane remains a later product API slice.

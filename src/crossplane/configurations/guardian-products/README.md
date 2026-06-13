# guardian-products Crossplane shape

Status: skeleton, not installed by `guardian up` yet.

This directory defines the product API shape Guardian is moving toward:

- `platform.guardian.dev/PublicHttpService` is the reusable envelope for a
  public HTTP workload: namespace, deployment, service, TLS custody, probes,
  metrics scrape identity, and alert profile.
- `products.guardian.dev/AisucksProduct` is a thin product declaration that
  composes one `PublicHttpService` in this hello-world slice.

The live implementation for this iteration is the direct-rendered platform
template at `src/platform/public-http-service/`. That is intentional: the
first release exercises the runtime and release gate without installing an
unpinned Crossplane function or provider. The Crossplane install slice should
replace the direct renderer with a pinned `guardian-products` Configuration
artifact.

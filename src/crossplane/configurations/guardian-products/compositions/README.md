# Composition placeholders

The intended composition graph for this slice is:

```text
products.guardian.dev/AisucksProduct
  -> platform.guardian.dev/PublicHttpService
       -> Namespace
       -> Deployment
       -> Service
       -> optional probe Service
       -> Gateway routes
       -> Gatus endpoint
       -> OTel scrape identity
       -> vmalert rules
```

`guardian up` currently renders `PublicHttpService` directly via the
repo-owned platform template. Do not introduce a Crossplane composition
function or provider here until its package digest is pinned and mirrored into
Guardian's release artifacts.

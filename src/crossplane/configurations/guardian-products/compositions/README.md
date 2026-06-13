# Composition Sketch

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
repo-owned platform template. The platform EdgeGateway substrate has a pinned
Crossplane/provider/function path; this product composition should move only
when it can reuse that pinned substrate or ship as its own pinned package.

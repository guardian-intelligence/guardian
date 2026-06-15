# Company Site Architecture

This file applies to `src/products/company/**`.

The company site is the public Guardian Intelligence web surface:
`guardianintelligence.org`, `dev.guardianintelligence.org`, letters, news,
docs-like product pages, OG images, and later authenticated product entry
points. Optimize for a fast first document, reliable deploys, and boring
self-hosted operations.

## Target Shape

- The long-term web app is TanStack Start on React, served from the Vite+
  workspace. TanStack Start owns full-document SSR, streaming, route loaders,
  hydration, and fast post-SSR client navigation through TanStack Router.
- Vite+ is the package manager/build runner for the monorepo. It is not a
  runtime dependency in Kubernetes. Build with `vp install --frozen-lockfile`
  and `vp build` through Bazel targets.
- Directus is the content authoring backend and admin UI. It is not the public
  website serving tier.
- Kubernetes serves immutable OCI images built by Bazel. Public traffic should
  hit the company web app pods and their static assets, not Directus directly.

## Serving Model

Use this request path for public pages:

1. Gateway routes `:443` SNI and `:80` host traffic to the company service.
2. The company web server streams SSR HTML as early as possible.
3. The first document contains critical CSS inline.
4. Hydration enables TanStack Router client navigation after the initial
   document is useful.
5. Client-side progressive enhancement may fetch background data after paint,
   but core page text, metadata, canonical URLs, and OG tags must already be in
   the streamed HTML.

The current Go static service is a scaffold for the first public deployment. Do
not deepen it into a second web framework. When TanStack Start lands, replace
the static Go asset server with a Start server image while preserving the same
Kubernetes envelope: `Deployment`, `Service`, Gateway routes, probes, metrics,
and digest-pinned image rollout.

## Directus Contract

- Directus owns editorial workflow, schemas, drafts, users, asset upload
  metadata, and content APIs.
- The public app owns rendering, routing, SEO, OG output, cache behavior, and
  uptime.
- Public SSR must not block on Directus for normal published page views. Use a
  versioned published-content snapshot in the app image or a local runtime
  cache that can serve stale content while Directus is down.
- Directus reads on the request path are allowed for preview/admin-only routes.
  Published public routes should use snapshots or stale-while-revalidate style
  app-local cache.
- Directus write/webhook paths should trigger a Guardian release or reconcile
  operation that produces a new published snapshot. Treat content publication
  as a typed, auditable operation, not a hand-edited file.
- Directus assets should live in S3-compatible object storage, preferably R2
  for this project. Public image URLs must be stable and cacheable. OG images
  are first-class publish artifacts and should be generated deterministically
  at publish/build time.

## Directus Deployment

- Self-host Directus as its own Kubernetes Deployment when introduced.
- Pin the Directus image by digest and manage it through Bazel/OCI plumbing.
- Use Postgres for Directus data. Back it up and restore it through Guardian's
  normal offsite survival floor.
- Use S3-compatible object storage for uploads before public authoring depends
  on uploaded assets. Early private authoring may use the platform's local
  Directus storage mode, but public assets must not live only on a pod
  filesystem.
- Use Redis when Directus runs more than one replica or when cache/session/
  websocket coordination matters. A single Directus replica is acceptable for
  early authoring because the public site must keep serving without Directus.
- Deliver Directus secrets from OpenBao to Kubernetes Secrets. Do not check in
  admin credentials, API tokens, storage credentials, or database passwords.
- Run Directus schema migrations as explicit release/converge work. Do not make
  public app startup mutate Directus schema.

## Zero-Downtime Deploys

- The Crossplane/Kubernetes delivery target, observability flow, and SLO
  promotion structure live in `docs/architecture/company-site-delivery.md`.
- Public web pods run on the pod network behind the Gateway. Do not use
  hostNetwork for the company site.
- Run at least two replicas for public serving.
- Use rolling updates with `maxUnavailable: 0` and a small positive surge when
  the platform envelope supports it.
- Readiness should prove the app can serve the current local content snapshot
  and static assets. It should not require Directus to be healthy.
- Liveness should only prove the process is not wedged.
- Shutdown must be graceful: stop accepting new requests, drain in-flight SSR
  work, then exit before the grace period ends.
- Static assets must be immutable and content-hashed with long cache headers.
  HTML should be short-cache or no-store depending on whether it is snapshot
  or personalized.
- Deploys promote immutable digests. Never roll traffic to a mutable tag.

## Web Performance Rules

- First response critical path target: stay under 14KB Brotli-compressed when
  practical for public marketing/content pages.
- Inline all CSS while the total site CSS is tiny. If CSS grows materially,
  inline only critical CSS and ship one immutable hashed stylesheet.
- Do not inline large JavaScript. Hydration bundles are allowed, but the first
  document must be useful without waiting for them.
- Put the first discoverable LCP image or media reference early in the HTML.
- Render canonical tags, title, description, OG, Twitter card, and structured
  data during SSR or snapshot generation.
- OG images matter. Every public route that can be shared should have a stable
  OG image URL and deterministic generation path.
- Client navigation can be JS-based after hydration, but every public URL must
  be directly requestable and SSR-renderable.

## Vite+ And Bazel

- Add package dependencies through the Vite+ workspace catalogs and lockfile.
- Use `vp install`; do not run raw `pnpm install` for repo changes.
- Package build scripts should use `vp build` for app builds.
- Root/workspace orchestration may use `vp run -w build` or package-targeted
  `vp run`, but it should ultimately invoke the package `vp build`.
- Bazel build/test targets must include the generated web artifacts. A top-level
  `bazelisk build //:build` must leave the company package squared away.
- Kubernetes images must contain built output only. Pods must not run package
  managers, Vite dev servers, or markdown/content generation.

## TanStack Start Migration Plan

When replacing the static scaffold:

1. Keep the current public URL behavior: `/`, `/letters`, letter detail pages,
   future `/news`, health endpoints, and OG image routes.
2. Move route ownership into TanStack Router files.
3. Use Start loaders/server functions for server-only Directus access.
4. Keep published content available from a local snapshot or app-local cache.
5. Package the Start output into an OCI image through Bazel.
6. Preserve the `PublicHTTPService` deployment envelope and existing Gateway
   route model.
7. Add tests for SSR metadata, direct URL requestability, no large inline JS,
   and content snapshot fallback when Directus is unavailable.

## Avoid

- Do not expose Directus as the public read path for anonymous website traffic.
- Do not make the public site depend on Directus health for readiness.
- Do not add another frontend framework for this surface.
- Do not introduce sidecars.
- Do not use mutable CMS state as the only copy of share-critical OG output.
- Do not make markdown editing the long-term authoring workflow.

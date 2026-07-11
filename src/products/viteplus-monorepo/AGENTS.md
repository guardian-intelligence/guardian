# Company Site Architecture

This file applies to `src/products/viteplus-monorepo/**`.

The company site is the public Guardian Intelligence web surface:
`guardianintelligence.org`, `dev.gi.org`, `gamma.gi.org`, letters, news,
company pages, OG images, and later authenticated product entry points.
Optimize for a fast first document, reliable deploys, and boring self-hosted
operations.

## Current Shape

- The active web app is TanStack Start on React, served from the Vite+
  workspace. TanStack Start owns full-document SSR, streaming, route loaders,
  hydration, and fast post-SSR client navigation through TanStack Router.
- Vite+ is the package manager/build runner for the web workspace. It is not a
  runtime dependency in Kubernetes. Build the app through Bazel targets such as
  `aspect build //src/products/viteplus-monorepo/apps/guardianintelligence-web/site:image`.
- `//src/products/viteplus-monorepo/apps/guardianintelligence-web/site:image` is the deploy-facing compatibility label.
  It aliases the real TanStack image at `//src/products/viteplus-monorepo/apps/guardianintelligence-web:image`.
- Kubernetes serves immutable OCI images built by Bazel. Public traffic should
  hit the company web app pods and their static assets.

## Serving Model

Use this request path for public pages:

1. Ingress routes host traffic to the company-site Service.
2. The company web server streams SSR HTML as early as possible.
3. The first document contains critical CSS inline.
4. Hydration enables TanStack Router client navigation after the initial
   document is useful.
5. Client-side progressive enhancement may fetch background data after paint,
   but core page text, metadata, canonical URLs, and OG tags must already be in
   the streamed HTML.

Keep the Kubernetes envelope boring: `Deployment`, `Service`, `Ingress`, probes,
metrics, PDB, topology spread, and digest-pinned image rollout.

## Zero-Downtime Deploys

- Public web pods run on the pod network behind tenant ingress. Do not use
  hostNetwork for the company site.
- Run at least two replicas for public serving; current environments use three.
- Use rolling updates with `maxUnavailable: 0` and a small positive surge.
- Readiness should prove the app can serve local content and static assets.
- Liveness should only prove the process is not wedged.
- Shutdown must be graceful: stop accepting new requests, drain in-flight SSR
  work, then exit before the grace period ends.
- Static assets must be immutable and content-hashed with long cache headers.
- Deploys promote immutable digests. Never roll traffic to a mutable tag.

## Web Performance Rules

- First response critical path target: stay under 14KB Brotli-compressed when
  practical for public marketing/content pages.
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
- Use Vite+ commands through Bazel-managed actions for build/release paths.
- Package build scripts should use `vp build` for app builds.
- Bazel build/test targets must include the generated web artifacts. A top-level
  `aspect build //...` must leave the company package squared away.
- Kubernetes images must contain built output only. Pods must not run package
  managers, Vite dev servers, or markdown/content generation.

## Directus Contract

Directus is planned as the content authoring backend and admin UI. It is not the
public website serving tier.

- Directus owns editorial workflow, schemas, drafts, users, asset upload
  metadata, and content APIs.
- The public app owns rendering, routing, SEO, OG output, cache behavior, and
  uptime.
- Public SSR must not block on Directus for normal published page views.
- Directus write/webhook paths should trigger a Guardian release or reconcile
  operation that produces a new published snapshot.
- Deliver Directus secrets from OpenBao to Kubernetes Secrets.

## Avoid

- Do not expose Directus as the public read path for anonymous website traffic.
- Do not make the public site depend on Directus health for readiness.
- Do not add another frontend framework for this surface.
- Do not introduce sidecars.
- Do not use mutable CMS state as the only copy of share-critical OG output.
- Do not make markdown editing the long-term authoring workflow.

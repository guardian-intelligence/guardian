# Company site delivery architecture

Status: target structure for `guardianintelligence.org`, 2026-06-14. This
extends the live Gateway, observability, release, and product-API work without
adding a second control plane.

## Goal

Serve the public company site as a fast SSR application with static-site
properties on the first request:

- streamed HTML is useful before hydration;
- critical CSS is inline while CSS remains tiny;
- static assets are immutable and digest/hash addressed;
- public reads do not depend on Directus availability;
- deploys promote immutable artifacts from dev to gamma to prod by SLO
  evidence, not by mutable tags or manual checks.

## Control-plane shape

There is one Guardian control plane: Kubernetes plus Guardian/Crossplane
operators. Product services express desired state as Kubernetes APIs; they do
not each invent a service-local control plane.

Layer the site like this:

| Layer | Owner | Current state | Responsibility |
| - | - | - | - |
| Talos/Kubernetes | `guardian up` | live | Node bootstrap, Cilium, seed registry, OpenBao, pinned component manifests. |
| Platform substrate | Crossplane + provider-kubernetes | live for `EdgeGateway` | GatewayClass/Gateway/listeners, shared edge policy, future common platform envelopes. |
| Public service envelope | `platform.guardian.dev/PublicHttpService` | Crossplane XRD/Composition | Namespace, Deployment, Service, TLSRoute, HTTPRoute, health probes, metrics labels, rollout defaults. |
| Product declaration | `products.guardian.dev/CompanySite` | Crossplane XRD/Composition | Chooses image/content digest/domain and composes the public service envelope plus content backend references. |
| Release judge | release architecture M6 | design ratified | Reads artifact evidence and SLO gates, then advances channel pointers or rolls back. |

The key rule is that `PublicHttpService` is the lift-out boundary. The current
Go static site, the future TanStack Start/Nitro server, and any later renderer
should fit behind the same envelope: `Deployment`, `Service`, Gateway routes,
readiness, `/metrics`, and digest-pinned image rollout.

## Crossplane reference model

Use Crossplane as the Kubernetes-native platform API layer, not as the release
engine and not as an application framework.

The Crossplane primitives to model against are:

- XRD: defines the custom API schema for an XR.
- XR: one requested platform capability, such as `PublicHttpService` or
  `CompanySite`.
- Composition: renders the Kubernetes resources that satisfy that XR.
- Function pipeline: the default composition mode for non-trivial APIs. The
  current Crossplane docs describe Compositions as function pipelines; use
  `mode: Pipeline` unless the resource is deliberately trivial.
- Configuration package: an OCI package containing XRDs, Compositions,
  dependency metadata, and examples. Crossplane Providers and Functions are
  packages too.

References of record:

- Crossplane Compositions:
  `https://docs.crossplane.io/latest/composition/compositions/`
- Crossplane EnvironmentConfigs:
  `https://docs.crossplane.io/latest/composition/environment-configs/`
- Crossplane packages:
  `https://docs.upbound.io/manuals/packages/overview/`
- provider-kubernetes:
  `https://github.com/crossplane-contrib/provider-kubernetes`
- Upbound `platform-ref-aws`, for repo and test shape rather than AWS choices:
  `https://github.com/upbound/platform-ref-aws`

`provider-kubernetes` is the boring provider for Guardian's near-term use case:
it lets Crossplane manage arbitrary in-cluster Kubernetes objects through
`Object` managed resources. The provider account should receive only the RBAC
needed for the platform APIs installed on that site. Do not grant a universal
cluster-admin provider identity by default.

Use `function-go-templating` for the first rewrite because it matches the
existing EdgeGateway implementation and keeps rendered objects inspectable.
Use `function-environment-configs` to merge site-specific environment bags into
the function pipeline. Move to a custom Guardian composition function only
when Go templates become a poor fit: deep validation, complicated loops,
cross-resource computation, or typed CUE schema reuse that cannot be expressed
cleanly in template input.

## Site configuration refactor

Site configuration is split across pre-Kubernetes bootstrap facts and
post-Kubernetes desired state. Crossplane can own only the second group because
it runs after Kubernetes exists. The durable split is:

```text
src/sites/<site>/
  bootstrap.yaml         # physical facts and Talos input

src/crossplane/environments/<site>/
  environment.yaml       # EnvironmentConfig plus XR instances
```

`bootstrap.yaml` is consumed by `guardian up` before the API server exists. It
contains physical facts that must never be copied across boxes: MAC addresses,
disk serials, gateways, hostnames, endpoints, and Talos patch lists.

`environment.yaml` is the "bag of composition configuration" for a site. It
contains:

- one `EnvironmentConfig` labeled with `guardian.dev/site=<site>`;
- root platform XRs such as `GuardianSite`, `ObservabilityStack`,
  `EdgeGateway`, and `OCIRegistry`;
- product XRs such as `CompanySite`, `AisucksProduct`, and `StatusSurface`;
- SLO/synthetic declarations consumed by the observability compositions and
  release judge.

`guardian up` should shrink to this sequence:

1. Read `bootstrap.yaml`.
2. Generate/apply Talos machine config.
3. Ensure the seed registry, OpenBao, Crossplane, provider-kubernetes, and
   pinned composition functions are installed.
4. Push Bazel-built OCI layouts to the seed registry by digest.
5. Apply the site environment bundle.
6. Wait for root XRs and required capability XRs to report ready.

The important consequence: environments differ by XR specs and
`EnvironmentConfig` data, not by copied component templates. Dev, gamma, and
prod are different bags of composition input over the same platform APIs.

## Crossplane package layout

Keep platform APIs and product APIs separate even while they live in one
monorepo:

```text
src/crossplane/
  packages/
    guardian-platform/
      apis/                # XRDs
      compositions/        # Composition pipelines
      examples/            # small XR examples
      tests/               # render/schema tests
      crossplane.yaml      # package metadata
    guardian-products/
      apis/
      compositions/
      examples/
      tests/
      crossplane.yaml
  environments/
    dev/
    gamma/
    prod/
```

`guardian-platform` owns reusable capabilities: Gateway, public service
envelopes, secret projection, observability, registry, database/storage
bindings, and shared policy.

`guardian-products` owns product declarations that compose platform
capabilities: `CompanySite`, `AisucksProduct`, `StatusSurface`, and future
workload products.

Package these through Bazel as OCI artifacts and mirror them to the seed
registry. Runtime clusters should not pull Crossplane packages from the
internet. Today the repo pins `xpkg.crossplane.io` package digests; the durable
target is:

```yaml
apiVersion: pkg.crossplane.io/v1
kind: Function
metadata:
  name: function-go-templating
spec:
  package: registry.guardian.internal/function-go-templating@sha256:...
```

The same applies to Crossplane itself, provider-kubernetes,
function-auto-ready, function-environment-configs, and any future custom
Guardian composition functions.

## Platform APIs

Start with a small API surface and make it hard to bypass.

### `EdgeGateway`

Owns the shared edge substrate:

- Gateway namespace.
- GatewayClass.
- Gateway.
- listeners for passthrough TLS and Gateway-terminated HTTP/TLS.
- cert-manager ClusterIssuer/Certificate objects when Gateway termination is
  used.
- required ReferenceGrants when routes cross namespaces.

`EdgeGateway` is already the right kind of Crossplane object: shared,
dangerous to hand-edit, and slow-moving.

### `PublicHttpService`

Owns the standard public workload envelope:

- Namespace.
- Deployment.
- Service.
- optional probe Service for in-cluster checks.
- TLSRoute and HTTPRoute.
- health and readiness probes.
- resource requests, memory limits, and `GOMEMLIMIT`.
- rollout strategy.
- OTel scrape labels for the `public-http` job.
- standard security context.

Inputs are the product's desired identity and runtime contract: app name,
namespace, digest-pinned image, domain, ports, health path, replicas, resource
class, TLS mode, and SLO surface.

This is the correct lift-out target for the current Go static company site and
the future TanStack Start/Nitro server.

### `ObservabilityStack`

Owns the per-site telemetry substrate:

- OTel Collector.
- VictoriaMetrics.
- vmalert.
- Alertmanager.
- blackbox-exporter.
- Grafana.
- optional ClickHouse ledger.
- optional Gatus until the cross-site blackbox path fully replaces it.

Environment config supplies the site name, sibling probe targets, ClickHouse
enabled flag, alert routing, and retention/capacity choices. Product services
should not edit collector or vmalert YAML directly; they should declare SLO
intent that the observability composition renders into scrape config and rule
fragments.

### `SecretProjection`

Owns only Kubernetes delivery of OpenBao-backed secrets:

- namespace-scoped SecretStore objects.
- ExternalSecret objects.
- target namespace and key names.
- optional readiness checks.

It never owns secret values. OpenBao remains the source of truth.
Source: `src/crossplane/packages/guardian-platform/secret-projection.yaml`;
bootstrap-side Bao policy/value preparation:
`src/guardian-cli/cmd/guardian/secret_projection.go` and
`src/guardian-cli/cmd/guardian/bao_bootstrap.go`.

### `OCIRegistry`

Owns the local/public artifact registry surface:

- zot Deployment/Service.
- publisher secret projection.
- Gateway route.
- storage config.
- admission/publishing policy hooks as they arrive.

`npm` in OCI artifact paths continues to mean package format, not publisher.

### `DirectusInstance`

Owns content authoring infrastructure:

- Directus Deployment.
- Postgres binding or owned instance.
- Redis only when required.
- object storage references for uploads and generated images.
- OpenBao secret projections.
- optional admin route.
- backup/restore integration.

It must not make Directus the public read path for `guardianintelligence.org`.

### `CompanySite`

Product-level declaration for `guardianintelligence.org`:

- composes one `PublicHttpService`;
- references the content snapshot digest and asset/OG image digest set;
- declares public routes such as `/`, `/letters`, and `/news`;
- binds to a `DirectusInstance` for preview/publish workflows;
- declares SLO profile and synthetic routes.

`CompanySite` chooses product intent. `PublicHttpService` enforces platform
invariants.

### `SyntheticCheck` and `SLOProfile`

Declare what must be measured:

- public routes to probe;
- expected status codes and page markers;
- metrics names or standardized runtime profile;
- whether a signal gates promotion, pages, or only records evidence.

Crossplane should render the measuring apparatus. It should not decide
promotion. The release judge reads VictoriaMetrics, ClickHouse evidence, and
signed gate records.

## Composition mechanics

Prefer this pipeline shape for site/product XRs:

```yaml
apiVersion: apiextensions.crossplane.io/v1
kind: Composition
spec:
  mode: Pipeline
  pipeline:
    - step: load-environment
      functionRef:
        name: function-environment-configs
    - step: render-resources
      functionRef:
        name: function-go-templating
    - step: mark-ready
      functionRef:
        name: function-auto-ready
```

Use `EnvironmentConfig` for values shared across many XRs in one site:

- site name and cluster name;
- edge Gateway name/namespace/listeners;
- ACME issuer policy;
- standard resource classes;
- alert routing;
- sibling probe URLs;
- OpenBao mount/path conventions;
- seed registry host.

Use XR spec fields for values that are intrinsic to the requested capability:
app name, image digest, domain, route list, content snapshot digest, and
product-specific SLO profile.

Use provider-kubernetes `Object` resources for ordinary in-cluster Kubernetes
objects. Keep provider RBAC narrow by API family. A platform provider account
can manage platform namespaces, Gateway API, cert-manager, External Secrets,
and observability objects; a product provider account should manage product
namespaces and product-owned routes/services only.

## Industry organization

The durable industry pattern for polyglot monorepos with Kubernetes and
Crossplane is:

- app code, platform API definitions, and environment instances may live in one
  monorepo;
- ownership boundaries are enforced by build targets, API packages, and
  visibility rather than by separate repositories first;
- product teams consume small platform APIs, not raw Helm values;
- environments are XR instances and environment config, not copied
  Deployments;
- Compositions are tested like code with render/schema/e2e tests;
- release artifacts are immutable digests;
- GitOps or a site-local operator applies desired state;
- Crossplane reconciles capability state, while release promotion remains a
  separate policy engine.

For Guardian, the Bazel package graph is the monorepo boundary of record.
Keep language-specific packages near their product code, keep Crossplane APIs
under `src/crossplane/packages`, keep bootstrap facts under `src/sites`, and
keep generated manifests as build artifacts unless an applied artifact must be
audited in git.

## Kubernetes object structure

For the company site, the desired runtime objects are:

- `Namespace company`.
- `Deployment company-site`, pod-network only, at least two replicas on public
  serving sites.
- `Service company-site` for Gateway backends.
- `Service company-site-probe` for in-cluster TLS/self probes that cannot
  hairpin through the host-network Gateway path.
- `TLSRoute company-site` for SNI passthrough when the app owns its certs.
- `HTTPRoute company-site-http` for `:80`, ACME HTTP-01 where applicable,
  health, and redirects.
- Pod labels:
  - `platform.guardian.dev/metrics-scrape: "true"`
  - `platform.guardian.dev/metrics-port: "9090"`
  - `platform.guardian.dev/slo-surface: public-http`

The platform envelope should eventually set `RollingUpdate` with
`maxUnavailable: 0` and positive surge for pod-network public services.
Readiness must only require the app to serve its local published content
snapshot and static assets. It must not require Directus to be healthy.

## Directus placement

Directus is the CMS/admin backend, not the public read path.

Run it as ordinary Kubernetes components when introduced:

- `Deployment directus`, digest-pinned.
- Postgres for Directus state, backed up through Guardian's normal survival
  floor.
- Initial single-node uploads use an explicit hostPath; the durable target is
  S3-compatible object storage before public authoring depends on uploads.
- Redis only when Directus needs multi-replica coordination, websocket
  coordination, or cache/session sharing.
- R2/S3-compatible object storage for uploads and generated public images.
- OpenBao as source of truth for database, admin, storage, and webhook secrets,
  projected to Kubernetes Secrets.

Public pages should read from a published snapshot in the web image or an
app-local cache that can serve stale content. Directus webhooks should create a
typed publish/reconcile operation that produces a new content snapshot and OG
image set. Preview/admin routes may read Directus directly; public SSR should
not block on it.

## Serving path

The target request path is:

1. Cilium Gateway receives `:443`/`:80`.
2. Gateway routes by SNI/host to `company-site`.
3. TanStack Start streams SSR HTML from the server pod.
4. The document contains route metadata, canonical URLs, OG/Twitter tags,
   structured data, critical CSS, and early LCP image references.
5. Hydration enables TanStack Router client navigation.
6. Background progressive enhancement fetches non-critical data after the page
   is already useful.

Server-side route loaders may use Directus for preview or background refresh
work, but published pages should use the local snapshot/cache. Client-side
code should not call Directus anonymously for core public content.

## Observability stack

Copy the Verself pattern at the architecture level, not its Nomad mechanics:
OTel Collector is the telemetry spine, VictoriaMetrics is the hot SLO plane,
and ClickHouse is the wide-event forensics plane.

The concrete flow for public web services is:

1. The Start/Nitro server exposes bounded Prometheus/OpenMetrics metrics on
   its diagnostics port, currently `:9090`.
2. `PublicHttpService` labels opted-in pods with scrape metadata.
3. OTel Collector discovers those pods through the `public-http` Kubernetes
   service-discovery scrape job.
4. OTel remote-writes metrics to VictoriaMetrics.
5. `vmalert` evaluates service and rollout rules from VictoriaMetrics.
6. Alertmanager/ntfy handles paging, and the release judge queries the same
   VictoriaMetrics data for promotion decisions.

ClickHouse remains the cold plane:

- container logs and Kubernetes Events already flow through OTel on ledger
  sites;
- future server traces should arrive via OTLP on a non-public listener and be
  scrubbed before ClickHouse export;
- high-cardinality request, content-publish, and deploy forensics belong in
  ClickHouse wide events, not VictoriaMetrics labels.

For browser SLOs, keep the browser write path first-party:

- the web client sends Web Vitals and navigation timing to a company-site
  endpoint;
- the server validates and bounds dimensions;
- the server emits low-cardinality SLO metrics to VictoriaMetrics and detailed
  wide events to ClickHouse.

Browsers should not write directly to VictoriaMetrics or ClickHouse.

## Promotion flow

Promotion is by immutable subject and signed evidence:

1. Build creates a web image digest and a content snapshot/asset digest set.
2. Dev follows the edge candidate and converges it.
3. Dev gates prove the artifact is installable and observable.
4. Gamma admits the same candidate, runs the release gate, and signs
   `gate-pass` or `gate-fail`.
5. Prod admits only a candidate with provenance, gamma gate-pass, no taint, and
   available error budget for non-reliability changes.
6. Prod post-promote watch either records `deployed` or rolls back by pointer
   move to last good.

The gate should use the hot plane for decisions and the cold plane for
debugging:

- VictoriaMetrics: availability, 5xx rate, request latency, scrape health,
  restart deltas, rollout state, synthetic probe success, Web Vitals once
  emitted.
- ClickHouse: logs, traces, content-publish records, deploy markers, raw RUM
  events, and failed-request forensics.
- Kubernetes status: Deployment availability, observed generation, readiness,
  and Gateway route admission.

Gate-result records are release artifacts. They should be machine-readable and
signable, matching `docs/architecture/release.md`.

## Initial SLO gate set

Use conservative, boring signals first:

- `up{job="public-http", app="company-site"} == 1` for every ready pod.
- Sibling blackbox `probe_success == 1` for `/`, `/letters`, `/news`, and
  `/healthz` once those routes are public.
- No sustained 5xx responses for public routes.
- Readiness stays green through the rollout.
- Restart delta is zero outside deliberate rollout replacement.
- OTel, VictoriaMetrics, and vmalert are healthy enough to make the gate
  decision.
- ClickHouse ingest is healthy on ledger sites; a cold-plane outage blocks
  promotion but should not make already-serving public pages fail readiness.

As the Start app lands, add route-level SSR timing, first-byte timing, LCP, INP,
and content snapshot freshness. Keep label cardinality bounded: route pattern,
site, app, status class, method, and surface are enough for the hot plane.

## Rollout constraints

- No sidecars.
- No public traffic to Directus.
- No package managers, Vite dev servers, or content generation inside serving
  pods.
- No mutable tags in deploy state.
- No readiness dependency on Directus, ClickHouse, or VictoriaMetrics.
- No high-cardinality data in VictoriaMetrics labels.
- No browser-direct telemetry writes to observability databases.

## Migration order

1. Split site config into bootstrap facts and Crossplane environment bags.
2. Package the existing EdgeGateway composition as the first
   `guardian-platform` package.
3. Move observability site flags into `ObservabilityStack`,
   `SyntheticCheck`, and `SLOProfile` XRs, including the `public-http`
   scrape path, company-site vmalert rules, and blackbox targets after
   public DNS/routes are stable.
4. Mirror Crossplane packages, provider-kubernetes, and composition functions
   into the seed registry.
5. Replace the static service image with a TanStack Start/Nitro server image
   that preserves `/healthz`, `/livez`, `/metrics`, and the same Gateway shape.
6. Expand Directus CMS/admin with object storage, preview routes, OpenBao
   secrets, and publish webhooks.
7. Move content publication from markdown edits to a typed snapshot-producing
   release operation.
8. Wire release judge promotion from dev to gamma to prod using the SLO gates.

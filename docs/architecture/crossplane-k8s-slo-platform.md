# Crossplane/Kubernetes SLO platform

Status: current target structure, 2026-06-15. This is the operating shape for
Guardian public sites on Crossplane platform APIs.

## Goal

Run `guardianintelligence.org` as an ultra-fast TanStack Start/Nitro public
site with Directus as the authoring backend, immutable build artifacts, and
automated SLO promotion from dev to gamma to prod.

Public reads must not depend on Directus, ClickHouse, VictoriaMetrics, or a
package manager. A serving pod should only need its image, local content
snapshot, static assets, and certificate material to answer public requests.

## Repository shape

Keep pre-Kubernetes facts, platform APIs, product APIs, and environment
instances separate:

```text
src/sites/<site>/
  bootstrap.yaml                  # physical facts for guardian up and Talos

src/crossplane/
  packages/
    guardian-platform/
      edge-gateway.yaml
      secret-projection.yaml
      public-http-service.yaml
      directus-instance.yaml
    guardian-products/
      aisucks-product.yaml
      company-site.yaml
  environments/
    dev/environment.yaml          # EnvironmentConfig plus site XRs
    gamma/environment.yaml
    prod/environment.yaml

src/infrastructure-components/    # substrate and pinned image re-exports
src/products/<product>/            # application code and product-owned builds
```

`bootstrap.yaml` stays physical: hostnames, IPs, MACs, disk serials, Talos
schematics, gateways, and cluster bootstrap inputs. Crossplane never owns these
because Crossplane only exists after Kubernetes exists.

`environment.yaml` is post-Kubernetes desired state: one `EnvironmentConfig`,
root platform XRs, product XRs, secret projections, SLO declarations, and
synthetic route declarations.

## Control-plane layers

There is one control plane: Kubernetes plus Guardian/Crossplane APIs. Product
services consume platform APIs; they do not create service-local control planes.

| Layer | Owner | Responsibility |
| - | - | - |
| Talos and bootstrap | `guardian up` | Install Talos, create the single-node cluster, seed OpenBao/Crossplane/provider-kubernetes, push OCI layouts to the seed registry. |
| Environment bundle | `src/crossplane/environments/<site>` | Site-specific desired state and configuration bag. |
| Platform package | `guardian-platform` | Gateway, public HTTP envelope, secret projection, observability, Directus, registry/database/storage bindings. |
| Product package | `guardian-products` | Product intent such as `CompanySite`, `AisucksProduct`, and future workload products. |
| Release judge | Guardian release tooling | Promote immutable artifacts by signed evidence and SLO gates. Crossplane reconciles state; it does not decide promotion. |

Use Crossplane function pipelines for non-trivial APIs:

```yaml
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

`guardian up` pushes local OCI layouts, renders digest refs into the environment
bundle, and applies XRs. Crossplane owns the stable Kubernetes envelopes behind
XRDs and Compositions.

## Platform APIs

`EdgeGateway` owns the shared edge substrate: GatewayClass, Gateway, listeners,
Gateway TLS policy, cert-manager issuers/certificates, and ReferenceGrants.

`SecretProjection` owns Kubernetes delivery of OpenBao-backed secrets:
namespace-scoped SecretStores and ExternalSecrets. It never owns secret values.
PR 33 made this the path for observability and zot secrets; do not reintroduce
bespoke ExternalSecret manifests for those classes of secrets.

`PublicHttpService` is the public workload envelope. It owns Namespace,
Deployment, Service, probe Service, Gateway routes, probes, resource defaults,
standard security context, and observability labels. The current Go company
site, future TanStack Start/Nitro site, and Aisucks API should all fit behind
this envelope.

`ObservabilityStack` should own the per-site telemetry substrate: OTel
Collector, VictoriaMetrics, vmalert, Alertmanager, blackbox-exporter, Grafana,
kube-state-metrics, optional ClickHouse, and temporary Gatus while the
cross-site blackbox path finishes replacing it. The first implemented slice
uses this XR as the source for the ClickHouse ledger ratchet.

`DirectusInstance` owns the authoring backend: Directus, Postgres binding,
PVC-backed local uploads, optional S3-compatible object storage references,
optional Redis, admin route policy, backup/restore hooks, and OpenBao secret projections. It
must not become the public read path.

`SyntheticCheck` and `SLOProfile` describe the routes and metrics that gate
promotion. Crossplane renders the measurement apparatus; the release judge
evaluates the evidence.

Product APIs compose platform APIs. `CompanySite` declares domain, image
digest, content snapshot digest, route list, Directus binding, and SLO profile,
then composes one `PublicHttpService` plus the synthetic checks for `/`,
`/letters`, `/news`, and `/healthz`.

## Kubernetes runtime shape

The public site should run as pod-network workloads behind Cilium Gateway:

- `Namespace company`.
- `Deployment company-site`, two replicas for public serving sites.
- `Service company-site` for Gateway backends.
- `Service company-site-probe` for in-cluster probes that should not hairpin
  through the node's host-network path.
- `HTTPRoute company-site-https` for platform-terminated HTTPS.
- `TLSRoute company-site` only when the app-owned certificate mode is selected.
- `HTTPRoute company-site-http-redirect` for platform-terminated port 80
  redirects.
- `HTTPRoute company-site-http` for passthrough port 80 forwarding when the app
  owns redirects.
- Standard scrape labels:
  - `platform.guardian.dev/metrics-scrape: "true"`
  - `platform.guardian.dev/metrics-port: "9090"`
  - `platform.guardian.dev/slo-surface: public-http`

Use rolling updates with `maxUnavailable: 0` and positive surge for pod-network
public services. This gives application-level zero-downtime deploys on a
healthy node. It does not make a single-node site highly available against node
loss; sibling blackbox probes and fast rollback are the current mitigation.

Readiness must mean "this pod can serve the published local snapshot." It must
not require Directus, ClickHouse, VictoriaMetrics, Grafana, or an external API.

## Serving model

First request:

1. Cilium Gateway routes host/SNI to `company-site`.
2. TanStack Start/Nitro streams useful SSR HTML.
3. HTML contains canonical metadata, OG/Twitter tags, structured data, critical
   CSS, and early LCP image references.
4. The response does not wait on Directus. Published content comes from the
   image or an app-local immutable/stale-servable snapshot.

After first paint:

1. TanStack Router hydrates and enables client-side navigation.
2. Background progressive enhancement fetches non-critical data.
3. Preview/admin paths may read Directus directly; anonymous public content
   paths should not.

Directus publish webhooks should create a typed publish operation that renders
a new content snapshot and OG image set. The site deploys or refreshes by
digest, not by live Directus reads.

## Observability copy from Verself

Copy the architecture, not the Nomad mechanics:

- OTel Collector is the telemetry spine.
- ClickHouse is the wide-event forensics store for logs, traces, content
  publish events, deploy markers, and detailed browser events.
- VictoriaMetrics is the hot SLO store for low-cardinality numeric signals.
- vmalert evaluates PromQL rules, Alertmanager routes pages, and
  blackbox-exporter supplies sibling-site external vantage checks.

The important split is that promotion decisions read VictoriaMetrics, while
debugging and audit read ClickHouse.

## Server to VictoriaMetrics

Every public web server, including the future TanStack Start/Nitro server,
must expose bounded Prometheus/OpenMetrics metrics on its diagnostics port.
The required low-cardinality labels are `site`, `app`, `route_pattern`,
`method`, `status_class`, and `slo_surface`. Do not put URL, user, IP, content
ID, trace ID, or Directus object IDs into VictoriaMetrics labels.

The path is:

1. Server exposes `/metrics` on `:9090`.
2. `PublicHttpService` labels the pod for discovery.
3. OTel Collector's `public-http` scrape job scrapes `<podIP>:9090`.
4. OTel Collector remote-writes to site-local VictoriaMetrics.
5. vmalert and the release judge query VictoriaMetrics for SLO gates.

This is separate from OTLP traces. Traces can derive useful RED metrics later,
but explicit server metrics are the gate source of record because they are
bounded, query-cheap, and resilient to trace sampling or ClickHouse lag.

## Server/browser to ClickHouse

Verself's TanStack/Nitro pattern should be retained for forensics:

- Nitro initializes a curated OTel SDK at process start.
- Nitro request hooks attach route, server-function, correlation, status, and
  duration attributes to spans and structured logs.
- Browser tracing uses a same-origin OTLP HTTP proxy route so CSP can keep
  `connect-src 'self'`.
- Browser runtime/CSP/reporting events post to same-origin routes, where the
  server validates body size and schema before emitting structured logs/spans.
- OTel Collector receives or tails those signals, scrubs attributes, and writes
  the wide events to ClickHouse.

Browser code must not write directly to ClickHouse or VictoriaMetrics.
High-cardinality details stay in ClickHouse attributes/events; only bounded
aggregates become metrics.

## Promotion flow

Promotion is by immutable artifact and signed evidence:

1. Build creates the TanStack/Nitro server image, content snapshot digest, and
   static asset/OG image digest set.
2. Dev follows the candidate channel and converges through Crossplane.
3. Dev gate verifies installability, route health, scrape health, and basic
   telemetry.
4. Gamma admits the same candidate digest and runs the release SLO gate.
5. The release judge records a signed `gate-pass` or `gate-fail` artifact.
6. Prod admits only candidates with provenance, gamma `gate-pass`, no active
   taint, and enough error budget for the change class.
7. Prod post-promote watch records success or rolls the channel pointer back
   to the last-good digest.

Initial gate signals:

- Deployment observed generation and availability are current.
- `up{job="public-http", app="company-site"} == 1` for every ready pod.
- Sibling blackbox `probe_success == 1` for `/`, `/letters`, `/news`, and
  `/healthz`.
- No sustained 5xx rate for public routes.
- Restart delta is zero outside expected rollout replacement.
- vmalert, OTel Collector, and VictoriaMetrics are healthy enough to evaluate
  the gate.
- ClickHouse ingest is healthy on ledger sites; a cold-plane outage blocks new
  promotion evidence but must not make already-serving pages fail readiness.

Later gates add route-level SSR duration, TTFB, Web Vitals, content snapshot
freshness, OG image existence, canonical/structured-data checks, and Directus
publish evidence.

## Rollout order

1. Add `ObservabilityStack`, `SyntheticCheck`, and `SLOProfile` APIs.
2. Move site observability config from `guardian up` structs into the
   environment bundles.
3. Mirror Crossplane packages, providers, and functions into the seed registry.
4. Replace the Go static company site with the TanStack Start/Nitro image while
   preserving `/healthz`, `/livez`, `/metrics`, and the same Gateway contract.
5. Introduce Directus publish operations that create content snapshots and OG
   image artifacts.
6. Wire dev to gamma to prod promotion through signed gate-result artifacts.

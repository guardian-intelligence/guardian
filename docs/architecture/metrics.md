# Metrics: v2 skeleton

Status: specification, 2026-06-13. Companion to
`docs/architecture/observability.md`.

This slice intentionally measures only the public page and health endpoints.
Product-write, dependency, SDK synthetic, and store metrics return with the
Connect runtime and database/verifier slices.

## Constraints

- Label values must never contain client IPs, URLs, request bodies, or user
  payloads. `handler` is a closed-set route pattern, never the raw path.
- Release identity stays out of the binary. The image digest is the release
  identity; Kubernetes and the Guardian converge event carry it.
- The diagnostics listener is private to the cluster/host scrape path. The
  public runtime surface remains `/`, `/healthz`, and `/livez`.

## Catalog

| metric | type | labels | notes |
|---|---|---|---|
| `aisucks_build_info` | gauge | `version` | Static build metadata for scrape presence checks. |
| `aisucks_http_requests_total` | counter | `handler`, `method`, `code` | Public HTTP request count. Handler values are route patterns such as `GET /{$}` and `GET /healthz`. |
| `aisucks_http_inflight_requests` | gauge | — | Drain and saturation visibility during rollouts. |
| `go_*`, `process_*` | — | — | Added when the app adopts the standard Go collectors. |
| ksm / cAdvisor / hubble / `probe_*` / otelcol / VM self | — | — | Platform scrape jobs. |

## Rules

- `AppErrorRate`: `sum(rate(aisucks_http_requests_total{code=~"5..",handler!="GET /healthz"}[5m])) > 0` for 5m.
- `ScrapeTargetDown`, `PodNotReady`, `PodRestartStorm`, and platform rules stay owned by `vmalert`.

## Verification

- Unit tests drive the public handlers and metrics shape.
- Render tests pin the `public-http-service` envelope for pod-network and
  host-network sites.
- Fleet gates check `/healthz` and the charter page marker until Connect
  Health is publicly served.
- After converge, VictoriaMetrics must show `up{job="aisucks"}` for the
  expected pod identity: per-pod on pod-network sites, loopback on host-network
  sites.

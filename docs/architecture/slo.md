# SLOs and the public status page

Status: skeleton specification, 2026-06-13.

The current aisucks surface is deliberately small: the charter page, health
endpoints, and the hello API used by the SDK and release gate. Public SLOs for
product writes, review flow, dataset freshness, and durability return when
those products exist.

## Principles

- Public SLOs describe user-visible behavior, not internal topology.
- Synthetic traffic is first-class because organic traffic is sparse.
- Sibling blackbox probes are the external-vantage signal; site-local metrics
  prove the serving process and rollout state.

## SLIs

Rolling 30-day windows once recording rules land.

| # | SLI | Spec | Implementation | Vantage |
|---|---|---|---|---|
| 1 | Page availability | The page answers. | non-5xx ratio for `GET /{$}` plus sibling `probe_success` | server + sibling |
| 2 | Hello availability | The SDK hello call answers. | non-5xx ratio for `GET /api/v1/hello` plus Gatus hello probe | server + sibling |
| 3 | Rollout health | The deployed workload converges. | Kubernetes rollout state, `up{job="aisucks"}`, restart delta | site-local |

## Targets

Targets remain conservative while each site is single-node:

- Page availability: 99.9% / 30d.
- Hello availability: 99.9% / 30d.
- Rollout health: no sustained failed rollout outside an active deployment.

## Page

The shipped status page remains a light switch: one `isDeploying` boolean per
workload, grouped by namespace, rendered as TOML/JSON/YAML/HTML. It does not
expose raw dashboards or inventory.

# SLOs and the public status page

Status: specification, 2026-06-12. Targets marked RATIFY are operator
decisions pending sign-off; everything else is implementable now. Companion
to `docs/architecture/metrics.md` (the instruments) and
`docs/architecture/observability.md` (the pipeline). The charter (value 5)
already requires SLOs with error budgets; this doc gives them numbers and a
public rendering: `status.guardianintelligence.com`.

## Principles

- **Public SLOs, not public dashboards.** The page renders judgments
  (SLI ratios, budgets, release state) — never a live query surface, never
  raw inventory. Operator depth (per-pod detail) stays in Grafana behind
  port-forward: not secret, just recon surface and noise.
- **One truth.** The pager, the release gate, and the public page read the
  same vmalert recording rules. No parallel arithmetic.
- **SLIs are ratios, not percentiles.** "99% of requests under 500ms" =
  histogram bucket counter over total. Percentiles are diagnostics and stay
  on operator dashboards.
- **Vantage is disclosed.** Server-side measurement cannot see DNS/network
  failure; sibling blackbox probes can. The page states where each number is
  measured from, and that a total-fleet outage takes the page down too
  (three-site serving makes that the only blind spot; pretending otherwise
  is the theater this page exists to kill).
- **Synthetic traffic is first-class.** At our volume, user traffic is too
  sparse for stable ratios. Sibling probes (~175k observations/30d) are
  valid events for availability SLIs, disclosed as such. User traffic
  dominates latency/funnel SLIs as it grows.

## SLIs (spec → implementation)

Rolling 30-day windows. All implementations are recording rules over the
metrics catalog (docs/architecture/metrics.md).

| # | SLI | Spec (user sentence) | Implementation | Vantage |
|---|---|---|---|---|
| 1 | Page availability | "The page answers." | non-5xx ratio: `aisucks_http_requests_total{handler="/{$}"}` AND `probe_success` from siblings | both, shown separately |
| 2 | Page latency | "The page is fast." | ratio of `/` requests with `le="0.5"` over total | server-side; probe duration as external check |
| 3 | Submit success | "Hitting Enter works." | non-5xx ratio on `handler="/report"`. 502s from upstream fetch failures COUNT AGAINST US — the submitter experienced a failure regardless of whose fault | server-side |
| 4 | Dependency (published, not budgeted) | "How OpenAI's share infrastructure is doing, measured by us." | `aisucks_fetch_total{reason="ok"}` ratio + fetch duration | server-side egress |
| 5–7 | Reserved | time-to-verdict, dataset freshness, durability (charter value 5) | placeholders rendered as placeholders until the products exist | — |

## Targets (RATIFY)

Derived from architecture, not aspiration: single node per site (Talos
upgrades reboot the box), postgres deploys are Recreate, app deploys are
zero-downtime (proven v4).

- SLI 1 availability: **99.9% / 30d** (budget 43.2 min). 99.99% is
  architecturally dishonest for a single-node site; do not claim it before
  node redundancy exists. RATIFY.
- SLI 2 latency: **99% of `/` under 500ms** server-side. RATIFY.
- SLI 3 submit: **99.5% / 30d** (looser: synchronous ≤25s upstream fetch is
  in the path and 502s count against us). RATIFY.
- SLI 4: no target — it is a published observation of a third party.

Targets tighten on evidence, publicly (the page shows target AND achieved;
the history of target changes is the trust record).

## Error budget policy (the teeth)

- **Burn-rate alerting** (replaces crude thresholds as SLO data matures):
  fast burn 14.4× over 1h with 5m guard window → page; slow burn 1× over 3d
  → notify. Recording rules; same pipeline as every other page.
- **Budget gates releases:** the prod-promote gate consults remaining
  budget. Exhausted budget → feature releases freeze; only
  reliability-tagged releases ship (charter's freeze pattern applied to
  availability). Lands with the safe-rollouts brick (`guardian gate`).

## The page (operator = primary customer, public = same data)

**AMENDED 2026-06-12 (operator): two tiers, and the page is TanStack, not
no-JS.** The original static-snapshot design could never reflect an
availability loss in 100ms — and neither can the VM hot plane (15s scrape +
15s eval is a seconds-scale machine). So the page has two tiers with
disclosed cadences:

- **Live tier (milliseconds, availability only):** the status service runs
  its own high-frequency prober (keep-alive probes ~50ms cadence against the
  sites' public /healthz; all boxes are same-region so RTT ~1ms) with a
  green→amber→red state machine — first failed probe flips amber instantly
  (sub-100ms page reflex via SSE push, no refresh), ~3 consecutive failures
  within ~300ms confirm red. Achievable flip latency is failure-class
  dependent: RST/5xx ≤ ~75ms; blackhole bounded by the probe timeout
  (~150–200ms). The SAME probe outcomes export as /metrics, scraped into VM
  — the live light and the SLO history are one observation stream, two
  consumers (the one-truth principle survives the new tier).
- **Slow tier (seconds–days):** SLO ratios, budgets, release train — from VM
  recording rules on a 30–60s tick, SSR'd into the page.

**PHASING (2026-06-12, operator):** v0 ships the slow tier ONLY — the page
reads VM (the sibling `probe_success` series already flowing) and reflects a
loss at scrape+tick cadence, ~15–45s. The millisecond live tier is deferred;
when it comes, it is a presentation-edge concern (the prober/SSE design
above, or an Electric-style sync layer) consuming from the pipeline — never
a parallel metrics pipeline, never a stream processor in the alerting path
(observability.md records that decision; CH materialized views remain the
escape hatch for derived signals). The fleet view note: every site's VM
holds its siblings' probe results, so dev can serve an all-three-sites
availability dashboard from data it already has — while disclosing that
dev's own light is self-reported until multi-site serving lands.

Renderer: a Go module (`src/status/`) — prober + SSE hub (`/events`,
EventSource not WebSockets) + certmagic TLS + an embedded TanStack Start
shell (`viteplus-monorepo/apps/status-web`, same emit-static pipeline as
aisucks-web) whose hydrated island subscribes to SSE. JS is permitted here:
the no-JS constraint binds aisucks.app's product page; status is a guardian
surface (still no tracking, no analytics). Never proxies queries. End state:
served from ALL THREE sites, multi-A DNS; each site renders its own view
including sibling observations and names its vantage. THIN SLICE first:
dev-only, as the Cilium Gateway pilot — Gateway in TLS-passthrough mode
(TLSRoute, experimental channel; alpha CRDs precede the re-vendor; wipe
drill) makes Envoy a dumb SNI router on dev's :443, dispatching
dev.aisucks.app → aisucks pod and status.guardianintelligence.org → status
pod, each keeping its own certmagic. No cert-manager. Status-on-dev also
places the observer outside prod's failure domain, and the page must never
live inside the aisucks binary (it would restart on every product deploy —
the page exists to watch those). Page sections:

1. **SLO table** — per SLI: target, achieved (30d), budget remaining
   rendered in minutes ("12 of 43 minutes spent").
2. **Release train** — per site: current release (digest joined to tag via
   the signed release manifest, which is the page's data source — provenance
   and status converge), and pipeline state ("gamma: v9 soaking 7/10m ·
   prod: v8"). Deploy-in-progress banner derived from `kube_deployment`
   generation skew + `Converged` events.
3. **Release notes** — from the release manifest's notes field (annotated
   tag message until a forge exists), with signature verifiable.
4. **Incidents** — markdown files in `docs/incidents/`, rendered. Incident
   history is version-controlled and tamper-evident (the charter's
   append-only-record value applied to ourselves).
5. **Fleet** — per-site, per-component traffic lights (aggregates only).

Explicitly rejected: public Grafana (attack surface, unreadable), the page
before SLO definitions (dashboard cosplay), JS-based real-user measurement
(charter value 2; our external vantage is sibling probes, disclosed).

## Dependencies and sequencing

1. Metrics D1–D4 ship (instruments). 2. Ratify targets above. 3. Recording
rules for SLIs + budgets (+ burn alerts when data matures). 4. Renderer
module behind the paved road. **Public exposure blocks on the Cilium
Gateway round** — `status.guardianintelligence.com` is a new :443 tenant and
the aisucks binary owns :443 everywhere; the Gateway now carries five
dependents (OCI registry, status page, app egress lockdown, replicas:2,
weighted canaries) and is the next structural design session.

The end state this serves ("perfect hello world"): any workload shipped
through guardian inherits scrape, RED dashboard, SLO rows, deploy markers,
gates, rollback, and a status-page presence by convention — aisucks is the
reference tenant, not a special case.

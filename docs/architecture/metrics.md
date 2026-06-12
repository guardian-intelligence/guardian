# Metrics: the deployment-observability brick

Status: specification, 2026-06-12. Companion to
`docs/architecture/observability.md` (which owns the pipeline; this doc owns
what flows through it). Each deliverable below carries its own verification —
a deliverable without a check is an intention, not engineering.

## Method

RED per service (rate, errors, duration — the consumer's view), USE per
resource (utilization, saturation, errors — the machine's view). The USE half
already flows since v7: the default registry exports Go runtime + process
collectors (`go_goroutines` is the connection-leak detector; `go_memstats_*`
against GOMEMLIMIT), and cAdvisor covers cgroups. This brick adds the RED
half plus the domain funnel, and makes deployments first-class on dashboards.

Two standing constraints, inherited:

- **Charter value 2 applies to label values.** No IP, no URL, no share-link
  fragment may appear in any label. Every label is drawn from a closed set
  the code enumerates; `handler` is the ServeMux pattern (`r.Pattern`,
  Go 1.22+), never the raw path. Locked by test (D1.v3).
- **No `--stamp`.** Release identity stays out of the binary (stamping breaks
  digest reproducibility, the release gate's keystone). The image digest IS
  the version; `kube_pod_container_info{image_spec}` already carries it.

## Catalog

### Already flowing (v7)

| metric | labels | notes |
|---|---|---|
| `aisucks_reports_total` | `outcome`: accepted, duplicate, bounced, parse_failed, rejected | funnel head; series pre-created at start |
| `go_*`, `process_*` | — | default registry |
| ksm / cAdvisor / hubble / `probe_*` / otelcol / VM self | — | platform scrape jobs |

### D1 — serving plane (app RED)

| metric | type | labels | notes |
|---|---|---|---|
| `aisucks_http_requests_total` | counter | `handler`, `method`, `code` | handler = mux pattern or `other` (the 404 floor); method normalized to GET/POST/HEAD/other; both listeners (`:443` site, `:80` redirect+ACME) so scanner noise has a baseline |
| `aisucks_http_request_duration_seconds` | histogram | `handler` | classic buckets `.005 .01 .025 .05 .1 .25 .5 1 2.5 5 10 30` — the top end exists because `/report` holds the synchronous ≤25s liveness fetch; NOT native histograms (prometheus-receiver → remote-write → VM support for them is the bleeding edge of three projects; boring wins) |
| `aisucks_http_inflight_requests` | gauge | — | saturation + drain visibility during rollouts |

The funnel invariant (dashboard + test, not enforced in code):
`sum(aisucks_reports_total)` + 5xx + 413 + 422-rejected + 429 on
`handler="/report"` ≈ `aisucks_http_requests_total{handler="/report"}`.
The rate-limited path gains `outcome="ratelimited"` in `reportsTotal` so the
funnel sums; the 5xx paths stay out of the funnel by design (the existing
comment in web.go is right: the 502 itself is the signal, and now it is
actually counted).

### D2 — dependency plane (the one hard external dependency)

| metric | type | labels | notes |
|---|---|---|---|
| `aisucks_fetch_duration_seconds` | histogram | `source` | upstream share-page fetch; buckets `.1 .25 .5 1 2.5 5 10 20` (client timeout 20s) |
| `aisucks_fetch_total` | counter | `source`, `reason`: ok, http_status, network, body_too_large, no_conversation, parse | `reason` is the drift detector: a rising `http_status` share means chatgpt.com is challenging/blocking our UA; `no_conversation` is the soft-404 signature (the v6 incident, now a series) |
| `aisucks_parse_total` | counter | `source`, `strategy`: mapping, flight; `result`: ok, miss | format-drift detector — the flight parser exists because OpenAI changed framing once; this notices the next time |

### D3 — store plane

| metric | type | labels | notes |
|---|---|---|---|
| `aisucks_pgxpool_acquired_conns` / `_idle_conns` / `_total_conns` / `_max_conns` | gauge | — | hand-rolled GaugeFuncs over `pgxpool.Stat()` (no framework); with `go_goroutines`, the complete leak-detection pair the original crash-loop investigation lacked |
| `aisucks_pgxpool_acquire_duration_seconds_total` + `_acquire_count` | counter | — | pool contention as a rate |

Per-query duration is deliberately skipped: two queries exist (insert,
healthz) and `/report` duration already brackets them. Add on evidence.

### D4 — platform (guardian)

- **`Converged` event emission:** `guardian up` creates one Kubernetes Event
  per converge on the site's Node — reason `Converged`, message carrying one
  `name@digest` line per component pushed that run. Node-scoped, not
  per-workload: per-workload-kind involvedObject plumbing (Deployment vs
  StatefulSet) isn't worth it when describe-node shows the marker and the
  ledger will capture all events anyway. The k8s-native deploy marker;
  visible in `kubectl describe node` today, shipped to the ledger by
  k8sobjects when it lands (etcd forgets events after 1h; the ledger is the
  memory).
- **Deploy dashboard** (Grafana provisioning ConfigMap, dashboards-as-code):
  deploy markers (`changes(kube_pod_container_info[5m])` annotations) over
  RED panels, funnel, fetch-dependency panel, goroutines/memstats vs
  GOMEMLIMIT, restarts + node-boot side by side (the zfs-incident lesson).
- **Two rules** (each names its justifying incident, house style):
  - `AppErrorRate`: `sum(rate(aisucks_http_requests_total{code=~"5..",handler!="GET /healthz"}[5m])) > 0` for 5m
    → page. healthz is excluded — by its mux pattern `GET /healthz`, the
    handler label's actual value: its 503 during a DB outage is intended
    signaling, owned by PodNotReady. Justification: today a silent 5xx is
    invisible unless a probe happens to trip it.
  - `UpstreamFetchDegraded`: failure-share of `aisucks_fetch_total` > 50%
    over 15m, or fetch p95 > 10s for 15m → page. The share arm carries an
    activity floor (> 4 fetches per window): fetches happen only on user
    submissions, so at zero traffic the share is undefined and the rule is
    blind — an accepted gap until a periodic canary fetch ships — and
    without the floor one user's single dead link would page as a 100%
    share. Justification: the soft-404
    incident shipped bad rows for a day; an OpenAI block would silently kill
    the product's intake.

## Verification (what "done" means)

- **D1.v1 — lint:** `testutil.CollectAndLint` over the registry in a unit
  test; promlint-clean names or a recorded waiver.
- **D1.v2 — behavior:** table test drives httptest requests (stub store,
  fixture transport — both harnesses exist) and asserts exact counter deltas
  per outcome, including 413/422/429/502 paths.
- **D1.v3 — surface pinned:** scrape the test registry; assert every metric
  name is in the allowlist, every label value in its closed set, total series
  ≤ 400. The metrics surface is pinned the way TestMethodAndPathHygiene pins
  the routing surface. A canary string submitted as a link must not appear
  anywhere in the scrape (label-leak regression lock, charter value 2).
- **D2.v — drift senses:** fixture tests assert `reason`/`strategy` mapping:
  soft-404 fixture → `no_conversation`, flight fixture → `strategy="flight"`.
- **D3.v — live series:** after dev converge, query dev VM for
  `aisucks_pgxpool_max_conns` > 0 (proves collector → VM end to end; the
  scrape path itself is already covered by `up{job="aisucks"}`).
- **D4.v1 — event:** after dev converge,
  `kubectl get events -n default --field-selector reason=Converged` shows the
  component digests.
- **D4.v2 — rules load:** vmalert `/api/v1/rules` lists 10 rules, 0 errors.
- **D4.v3 — rule fires:** inject synthetic 5xx series into dev VM via
  `/api/v1/import` and watch `AppErrorRate` fire at its `for:` schedule, then
  resolve. No app fault required; the drill tests the rule, the route, and
  the page.
- **D4.v4 — marker renders:** the dev converge that ships this appears as an
  annotation on the deploy dashboard.

## Shipping

D1–D3 are one app release through the standard pipeline (gate covers
regression); D4 rides the same converge (manifests + guardian). The gate
queries this catalog enables (error-rate-clean-over-soak) belong to the next
brick (safe rollouts) and are out of scope here.

## Implementation notes (binding; from the 2026-06-12 design review)

- **Instrument per-route at registration** (curried vecs per handler), never
  by reading `r.Pattern` from an outer wrapper (mutation timing is
  incidental; 404/405 leave it empty). One outer wrapper owns ONLY the
  `other`/404 floor.
- **Three muxes, three decisions:** the site mux and mux80 are instrumented
  (`listener` label); the loopback diagnostics mux is NEVER instrumented
  (the collector's own scrapes must not become traffic). On mux80, an empty
  pattern with path prefix `/.well-known/acme-challenge/` counts as
  `handler="acme"` — otherwise every cert renewal looks like 404 noise.
- **Method strings are attacker-controlled label input.** Normalize to a
  closed set before labeling (either promhttp's built-in bounded method
  labeling or a hand-rolled allowlist). The pinned-surface test locks
  whichever set is emitted.
- **Empty ≠ 0 in PromQL.** A never-incremented counter doesn't exist; a
  `== 0` gate query passes on no-data for the wrong reason. Pre-create zero
  series (5xx for site handlers, every `reportsTotal` outcome including the
  new `ratelimited`, the chatgpt fetch/parse sets) and write verdict queries
  with `or vector(0)`.
- **Fetch `reason` increments at the branch points** inside
  fetchPage/chatgpt.go — `ErrGone` collapses four causes and classification
  cannot be recovered at the handler.
- **pgxpool collectors register at store construction** (pool non-nil).
  Diagnostics bind after the DB wait by existing design — `up==0` during a
  cold boot is intended and tolerated by ScrapeTargetDown's `for: 10m`.
- **Deploy markers use `kube_deployment_status_observed_generation`**
  increments (spec changes only). `changes(kube_pod_container_info[...])`
  fires on crashes and would paint deploys onto incident dashboards.
- **Converged events are imperative** (`kubectl create` with
  `generateName` — apply doesn't support it), post-converge, non-fatal on
  failure; etcd forgets them in 1h, so nothing durable depends on them
  before the ledger.
- **New vmalert rules go INSIDE the existing Go raw-string block** in
  vmalert.yaml.tmpl (two template languages share that file).
- **Rule drills:** prefer `vmalert-tool unittest` offline; live injection
  is dev-only with a `drill="true"` label (VM series deletion is awkward;
  residue ages out).
- **Tests:** the global registry means delta assertions and no
  `t.Parallel()` on metrics tests; `CollectAndLint` for naming; the
  pinned-surface test asserts names AND label values with series ≤ 400; the
  canary-string leak test scrapes after a submission and asserts absence.

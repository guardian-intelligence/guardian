# Load & synthetic testing playbook

The playbook for adding blackbox load and synthetic tests to any product
surface. Follow it and the conformance tests pass, results land where gates can
read them, and you cannot introduce a supply-chain leak. Deviate and CI stops
you before review.

k6 is the pinned tool (`@multitool//tools/k6`, one Go binary with a built-in
JS engine). It is the only load tool in the repo; do not add another.

## The model: three layers per surface

A surface earns "load-tested" by composing these, not by running one of them:

1. **Continuous correctness canary** — a continuous assertion that the real
   user flows work, alerting when the surface is broken. Two shapes: a
   `CronJob` running real flows for logic-bearing surfaces
   (`src/infrastructure/deployments/iam/login-canary/login-canary.js`, three
   Keycloak flows), or blackbox_exporter probes on a 15s scrape for HTTP
   surfaces (`deployments/alerting/{blackbox-exporter,synthetic-probes}.yaml`
   — two classes per stage: in-cluster Service and public-edge hairpin, with
   per-phase timing incl. TTFB).
2. **Deploy-time stress gate** — a k6 arrival-rate run against the *canary
   color* before traffic shifts, thresholds-as-code, wired as a Flagger
   `pre-rollout` webhook. Landing with company-site as first consumer; see
   "Next slice" below.
3. **Promotion-time assertion** — a Kargo `http` step that reads the source
   stage's recorded metrics and refuses to promote freight whose stress run
   did not pass. Live today for company-site:
   `deployments/guardian/promotion/pipelines/company-stage-gamma.yaml`.

## Hard rules (enforced by CI)

These are not style preferences — `//src/infrastructure/tests` fails the build
on each:

- **Scripts are `.js` files, never JavaScript inside a YAML string.** Inline
  scripts get no lint/typecheck, collide with Flux envsubst (k6 `${...}`
  template literals), and drift per-stage. Render the file into a ConfigMap
  with a kustomize `configMapGenerator`. Enforced by
  `TestNoInlineK6ScriptsInYAML`.
- **No network imports.** k6 resolves `k6`, `k6/http`, `k6/metrics`, ... from
  inside the pinned binary — those are not fetches. `import x from
  'https://...'` downloads and runs third-party code at run time, outside the
  digest-pinned image and the dark bundle, with the pod's credentials. Vendor
  what you need and import it by relative path. Enforced by
  `TestK6ScriptsHaveNoRemoteImports`.

Two more rules the platform depends on (keep them or the results are wrong):

- **Target in-cluster Services, never the Cloudflare edge.** Load against a
  `*.svc` name measures your surface; load against the public host measures
  Cloudflare and trips bot management. Edge behaviour is covered separately by
  `src/infrastructure/load/edge-health.js`.
- **Digest-pin the k6 image** (`docker.io/grafana/k6@sha256:...`), matching the
  k6 version in `src/tools/multitool.lock.json`. The image renders into the
  union lock and the dark bundle automatically because the manifest
  references it.

## Adding a test to a surface

1. **Write the script** next to the surface's deployment, e.g.
   `deployments/<vertical>/load/<name>.js`. If every stage runs the identical
   script, put it in one shared directory and render it per stage — see how
   `deployments/iam/login-canary/` feeds all three stages from one file.

2. **Tag every metric** so gates and dashboards can find the series:
   ```js
   // pin dynamic URLs to a static group, or k6 mints a new VictoriaMetrics
   // series per request value (a per-UUID delete leaked ~1440 series/day/stage
   // until it was fixed — see the loadtest-observability memory).
   http.del(`${base}/users/${id}`, null, { tags: { name: `${base}/users/{id}` } });
   ```
   Run k6 with `--tag stage=<stage>` (and `--tag surface=<name>`,
   `--tag scenario=<smoke|stress|headroom>` for load runs). Local runs must add
   `--tag run_source=local` so gate queries never see them.

3. **Render it** with a `configMapGenerator` in the stage kustomization,
   carrying the envsubst opt-out:
   ```yaml
   generatorOptions:
     disableNameSuffixHash: true
     annotations:
       # k6 JS template literals use ${...}; Flux envsubst must not touch them.
       kustomize.toolkit.fluxcd.io/substitute: disabled
   configMapGenerator:
     - name: <name>
       namespace: tenant-guardian-<stage>
       files:
         - <name>.js=../load/<name>.js
   ```

4. **Remote-write results** to VictoriaMetrics from the CronJob/Job:
   ```yaml
   args: [run, --tag, stage=<stage>, -o, experimental-prometheus-rw, /scripts/<name>.js]
   env:
     - name: K6_PROMETHEUS_RW_SERVER_URL
       value: http://vminsert-shortterm.tenant-root.svc:8480/insert/0/prometheus/api/v1/write
   ```
   Give the pod `automountServiceAccountToken: false`, a non-root
   `securityContext`, and a `CiliumNetworkPolicy` egress allowlist (kube-dns,
   the target Service, vminsert:8480, and `world:443` only if a flow needs the
   public host). Copy the pair at the bottom of
   `deployments/iam/login-canary.yaml`.

5. **Alert on it** with a `VMRule` in the surface's `observability.yaml`: a
   `Failing` rule on the failure/error series and an `Absent` rule so a canary
   that stops running still pages. Note one-shot runs emit a single sample per
   series, so use `sum_over_time`/presence, not `rate()`, over canary counters.

## Recording and reading results

Everything is a VictoriaMetrics series — nothing important should live only in
pod logs (the legacy `hey` webhook did, and its numbers were unreadable). Query
via the standard port-forward:

```
kubectl port-forward -n tenant-root svc/vmselect-shortterm 8481:8481
curl 127.0.0.1:8481/select/0/prometheus/api/v1/query \
  --data-urlencode 'query=max by (stage) (k6_http_req_duration_p99{surface="..."})'
```

Standard k6 series: `k6_http_reqs_total`, `k6_http_req_duration_p99`,
`k6_http_req_failed_rate`, `k6_vus`, plus any `Counter`/`Trend` your script
defines. All carry the tags you set in step 2.

## Running locally

Point the pinned k6 binary at an in-cluster Service through a port-forward — the
same script the CronJob runs, minus Kubernetes:

```
kubectl port-forward -n tenant-guardian-beta svc/<service> 8080:80
bazel run @multitool//tools/k6:workspace_root -- run \
  --tag run_source=local --tag surface=<name> --tag scenario=stress \
  -e TARGET_URL=http://127.0.0.1:8080 \
  src/infrastructure/deployments/<vertical>/load/<name>.js
```

Local numbers are valid only as before/after A/B on the same machine when you
are optimizing throughput. Absolute capacity comes from the in-cluster run,
where CPU, the network path, and replica count are real.

## Gates: where load results block promotion

Execution and assertion are split on purpose:

- **Kargo asserts, it does not execute.** Kargo's native steps cannot run pods,
  which is why we do not use its `spec.verification`/AnalysisTemplate machinery.
  A Kargo `http` step queries `vmselect` for the *source stage's* recorded
  series and fails the promotion if a threshold is unmet. Live pattern:
  `company-stage-gamma.yaml` checks the service-class `probe_success == 1`
  and `probe_duration_seconds` p95 ≤ 1.0s for the beta namespace before
  opening the promotion PR. A stress gate adds an error-rate-under-load and a
  TPS-floor check in the same shape, keyed on the freight's image digest so the
  gate proves *this* build was stress-passed.
- **Flagger executes.** The stress run itself is a Flagger `pre-rollout`
  webhook against the canary color; a breached threshold fails the webhook and
  Flagger rolls back and pages, hands-off.

## Next slice (not yet built — do not document as if it exists)

Landing with company-site as the first real consumer:

- A shared k6 profile library (`smoke` / `stress` / `headroom` arrival-rate
  scenarios + the tag conventions above) so a surface writes only its endpoints,
  request mix, target RPS, and SLOs.
- The company-site pre-rollout stress webhook + its Kargo TPS-floor gate.
- A `headroom` CronJob (beta/gamma) that ramps to the latency knee and records
  max-sustainable-TPS as a baseline, with a `VMRule` on drift.
- A conformance test requiring every HTTP surface to carry a stress gate, with a
  Warn+Audit VAP as the out-of-band-apply backstop.

Until then, follow the correctness-canary pattern above; the gate wiring slots
in without changing your script.

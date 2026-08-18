# Feature flags

Client feature flags for every product surface: seasonal events, pre-GA
testing of client features in production, and A/B experiments. Flags gate
**client code paths only** — house rule. Server binaries are never
runtime-reconfigured (impossible to reason about); services change by
rolling restart with a different OCI, shadow launch, and traffic
re-routing. Game-simulation and control-plane behavior ships as wasm
modules through the shadow-launch lane, never as flags.

## Architecture

Working backwards from the experience — merge a PR flipping
`winter-cosmetics` to GA, and every open client on every platform shows it
within ~2 minutes of merge, all within about a second of each other:

```
git (flags.json)
  └─ Flux → guardian-flags ConfigMap → kubelet mount refresh
       ├─ flagd (OFREP backend, replaceable)  ──  POST /features/ofrep/v1/evaluate/flags
       └─ feature-flags-notify (SSE)          ──  GET  /features/events
```

- **Source of truth**: `src/infrastructure/deployments/flags/prod/flags/flags.json`,
  applied by Flux as the `guardian-flags` ConfigMap (no name-suffix hash, so
  a flag flip is a data change, never a pod restart).
- **Evaluation**: OFREP — the vendor-neutral OpenFeature HTTP protocol —
  served by flagd and mounted same-origin at `/features` on each product
  host. Evaluation is server-side against the client's context (targeting
  key, etc.), so percentage rollouts and targeting rules never ship to the
  browser, and A/B bucketing is sticky per user. flagd is an implementation
  detail behind the OFREP contract; it can be swapped without touching a
  client.
- **Change notification**: `feature-flags-notify`
  (`src/flags/notify`) streams the *flag-set epoch* — a
  content hash of flags.json — over SSE at `/features/events`. No flag
  values on the stream, no per-user compute: one identical event fans out
  to every subscriber, and each client re-evaluates through OFREP with its
  own context. SSE is plain HTTP (`EventSource` is built into every
  browser, with auto-reconnect; native SSE clients are a few lines), which
  is what makes the contract uniform across platforms. The OFREP streaming
  ADR specifies SSE for exactly this job; when official providers ship it,
  the client glue is deleted and the endpoint contract stays.
- **Client kit** (web): `@openfeature/ofrep-web-provider` with polling
  disabled + ~10 lines of EventSource glue that bumps the evaluation
  context on an epoch event, forcing re-evaluation. The provider refreshes
  on tab-visibility for backgrounded pages. See
  `src/games/wake-up-mythra/web/src/flags/client.ts` for the reference wiring.
  Native apps use the official OFREP providers for their platform plus the
  same `/features/events` subscription.

Latency budget, measured (2026-08-09): merge → first client wave 117 s
(Flux reconcile + kubelet ConfigMap propagation dominate). Within a wave —
subscribers of one notify replica — client-to-client skew is sub-second
(measured 271 ms). Across replicas the waves are separated by up to one
kubelet sync period, because each node refreshes its ConfigMap mounts on
its own tick (measured 54 s apart). The same skew applies to the flagd
replicas: an epoch-triggered re-evaluation can race a still-stale
evaluator and keep old values, which is why the client glue schedules one
settling re-evaluation 90 s after each epoch event — the flip is then
durable even when the first fetch lost the race. Tab-visibility refresh
heals interactive sessions sooner. If tighter cross-fleet simultaneity is
ever needed, move both services off ConfigMap mounts to an API watch;
today's bound is fine for seasonal events and GA rollouts.

One operational caveat, learned the measured way: do not couple ingress
topology changes on a host with a flag flip in the same PR — the nginx
config reload can disturb established SSE streams at exactly the moment
the fan-out fires. Sessions converge on the next epoch, reconnect, or
visibility refresh, but the flip is no longer one clean wave.

### Verifying the lane

`wum-flags-canary` is a permanent boolean smoke test: the Wake Up Mythra
page mirrors it onto `<html data-flags-canary>`. Flip it in git and watch
an open session change attribute without a reload.

## Emergency rollback (breakglass)

The normal lane — revert the flag commit, merge, let Flux converge — takes
~2 minutes of pipeline plus review/CI time and is the right choice for
almost every incident.

When CI/Flux is unavailable or a client-side feature is actively hurting
users and minutes matter, edit the ConfigMap directly:

```sh
aspect infra auth --persona=write-basic       # device approval, audited
flux suspend kustomization guardian-flags-prod -n cozy-fluxcd
kubectl edit configmap guardian-flags -n tenant-guardian-prod
# flip the offending defaultVariant / state in flags.json
```

Both flagd and feature-flags-notify watch the mounted file: the edit
reaches every open client within kubelet's sync period (≤ ~60 s) plus the
sub-second SSE fan-out. **Suspending first is mandatory** — Flux would
otherwise reapply git's version of the ConfigMap at the next reconcile and
silently undo the rollback.

Then roll forward, immediately:

1. Land the same change in git (PR, merge).
2. `flux resume kustomization guardian-flags-prod -n cozy-fluxcd`
3. Confirm convergence (`aspect infra watch`) and that the live epoch
   matches the committed flags.json.

A suspended Kustomization is an outage of the GitOps lane, not a state to
park in: nothing under `deployments/flags/prod` converges while it is
suspended, and Alerta pages on suspended Kustomizations. If Keycloak is
down and `write-basic` cannot be minted, the x509 breakglass
(`--persona=root --reason "<why>"`) applies, pages a human, and is audit
logged.

Kill-switch tip: design risky client features with the flag as the *only*
gate (`enabled === true` renders, anything else — including evaluation
failure — renders nothing). Then the worst-case rollback is also just the
flag flip, and a flags-lane outage fails closed to the GA'd experience.

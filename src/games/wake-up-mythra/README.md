# Wake Up, Mythra!

See `docs/` for instructions outside the scope of development setup.

## Local development

Run

```sh
aspect mythra dev up # Provides no cluster access. See repo instructions for gaining access to the cluster to monitor changes post-merge.
```

Then open http://127.0.0.1:4254 and sign in as any name. The stack runs in
the background; drive it with:

```sh
aspect mythra dev status              # per-leg health, non-zero if unhealthy
aspect mythra dev logs                # recent lines from every leg
aspect mythra dev logs --leg=gateway  # follow one leg: pg|ch|otelcol|flagd|ingest|devissuer|park|gateway|web
aspect mythra dev smoke               # prove the stack end to end: a headless player connects and its telemetry lands
aspect mythra dev latency             # exercise every action; report client + authority latency from local telemetry
aspect mythra dev down                # stop everything
```

The legs:

| piece | where | notes |
| --- | --- | --- |
| journal db | repo-pinned PostgreSQL, 127.0.0.1:55432 | empty is complete: parks genesis themselves on first open |
| analytics db | repo-pinned ClickHouse, 127.0.0.1:59000 (native), :58123 (HTTP) | local `guardian_analytics` sink with the prod `events` + `otel_traces` schemas, on the exact server version the prod chart hard-pins |
| otel collector | 127.0.0.1:4317 (gRPC), :4318 (HTTP) | prod-shaped pipeline (redaction included) writing traces to the analytics db |
| flagd | 127.0.0.1:8016 (OFREP), :8013/:8014 | serves the committed prod flag set; hot-reloads on file edit |
| analytics ingest | 127.0.0.1:9636 | the real event Publish service, batching into the analytics db |
| dev OIDC issuer | 127.0.0.1:9635/realms/dev | Keycloak-shaped; signs any subject; the gateway validates it through the production `oidcGate` path |
| chunkies-chunkie | 127.0.0.1:9632 (sessions), :9631 (HTTP), :9637 (metrics) | one park authority and its journal |
| chunkies-gateway | 127.0.0.1:9634 (HTTP), :4433 (WebTransport), :9633 (metrics) | admission, public transport, static content, and the authenticated park proxy |
| web app | http://127.0.0.1:4254 | vite dev server proxying game requests to chunkies-gateway and events to ingest |

If another local tool owns a default port, override it consistently through
the launcher, for example `WUM_DEV_PG_PORT=55433
WUM_DEV_GATEWAY_HTTP_PORT=19634 WUM_DEV_GATEWAY_METRICS_PORT=19633 aspect
mythra dev up`. The same variables must accompany later `status`, `latency`,
and `down` commands for that stack.

Every telemetry lane a change emits is queryable locally: the `up` card
prints a copy-pasteable ClickHouse query (`guardian_analytics.events` for
product events, `guardian_analytics.otel_traces` for spans) and the
committed flags.json path for flag flips.

Nothing here bypasses production code paths: admission, module
distribution, terrain serving, and the netcode all run the same code they
run in the cluster. The only dev-specific piece is the issuer, and the gateway
only trusts it because `OIDC_ISSUER` says so.

## The edit loops

- **Web (TypeScript/React)**: save; vite HMR applies it. The web app lives
  at `src/games/wake-up-mythra/web`, a member of the repo-rooted
  vite-plus workspace (see `docs/web-workspace.md`).
- **Sim (Rust → wasm)**: `bazelisk run //src/chunkies/sim:refresh`
  while the stack runs. The new module lands in the behavior dir, the services
  hot-swap it, and connected clients follow the same update lane a
  production deploy uses.
- **Gateway/park (Go)**: `aspect mythra dev down && aspect mythra dev up`;
  clients redial and resync (also a prod-truthful path).

### Tick-rate latency drill

The local stack starts at 24Hz by default. Its server exposes a
development-only control that journals a live `rate_set`; it is server-side
truth, not a browser override. The connected client must consume that event,
re-anchor its clock at the event tick, and keep the same world and transport.

```sh
aspect mythra dev latency
```

The probe signs in once, exercises join, check-in, move and boost through the
real UI at 24Hz, asks the already-running authority to journal 48Hz, then
exercises move and boost again in the same page. `RATE_CHANGE` proves the page
identity stayed fixed, the world advanced, and there were no redials, resyncs,
restores, or reloads. It also reports two deliberately separate latency views:

- `CLIENT_ACTION`: first wire write until the journal event applies in the
  browser, from the production `wum.action` fact;
- `SERVER_ACTIONS`: server receipt until durable fan-out, with
  `queue_p*_ms` isolating the wait for the next authority tick.

Every row carries the rate the same connection actually observed, and the
command fails unless every client-observed action has a corresponding
completed `chunkies.intent` span in local ClickHouse. Raising the rate is
expected to shrink both the next-tick queue and the client's six-tick cushion
in milliseconds; journal commit and network RTT are not tick-rate work and
should not be credited to it. `WUM_DEV_TICK_HZ` changes the startup baseline
when a different experiment needs one; it is not used by this live handoff.

## Degradation harness

`netsim` is a dev-time UDP impairment proxy between the browser's
WebTransport dial and chunkies-gateway's QUIC listener. Chrome DevTools network
throttling does not touch QUIC, so this proxy is the only honest way to
degrade the game path. It simulates latency/jitter, packet loss, subway
tunnels (total silence), tower switches (path migration — the server
sees the connection arrive from a new source port), and interface
teardown (`/sever`). Jitter and loss draw from a seeded generator so
scenarios replay identically.

```sh
bazelisk run //src/chunkies/netsim &
WUM_DEV_PUBLIC_ADDR=127.0.0.1:14433 aspect mythra dev up   # dial via proxy
curl -X POST 'http://127.0.0.1:14434/impair?latency_ms=300&jitter_ms=40&loss_pct=5&seed=7'
curl -X POST 'http://127.0.0.1:14434/silence?ms=8000'
curl -X POST 'http://127.0.0.1:14434/migrate'
```

`src/games/wake-up-mythra/web/e2e/degradation.mjs` drives a full scenario
headlessly and asserts the client's behavior under each impairment —
including a deliberately RED-encoded assertion for the known
stuck-behind-after-CPU-stall crawl that the sim/clock module will flip.

## Database maintenance

```sh
scripts/wum-dev-db.sh wipe        # fresh empty journal
scripts/wum-dev-db.sh from-prod   # maintainers: wipe, then re-seed from the
                                  # prod journal through the JIT read-only
                                  # Mythra observer capability
                                  # to replay real park history locally
scripts/wum-dev-db.sh url         # print the local DSN
```

## Production operations

Authenticate once with the unattended read persona. The Aspect tasks mint
their own 15-minute, product-scoped capability only when needed. Token bytes
are never printed or placed in process arguments; the task removes its private
temporary kubeconfig as soon as the operation finishes.

```sh
aspect infra auth --persona=read
aspect mythra status
aspect mythra logs --leg=gateway --since=10m --tail=500
aspect mythra psql --query='SELECT * FROM park_events ORDER BY seq DESC LIMIT 10'
aspect mythra dump > mythra.sql
aspect mythra restart --leg=park
```

`status` and `logs` use standing read authority. `psql` and `dump` derive
`guardian-mythra-observer`, which can exec only `psql`/`pg_dump` in a fixed,
network-confined console carrying PostgreSQL's native `mythra_readonly` role.
`restart` derives `guardian-mythra-operator`, which can only delete a labeled
game component pod; the Deployment recreates the Git-declared revision. Neither
role can read Kubernetes Secrets, exec into the game or shared Postgres pods,
or touch payments/Directus. Scaling, image, behavior, and configuration changes
remain Git/Flux operations.

# Wake Up, Mythra! — development

The canonical guide to developing WUM: the one-command local stack, its
edit loops and harnesses, and the production operations verbs. The plan of
record and architecture invariants are `docs/wake-up-mythra-development.md`;
the simulation contract is `docs/netcode.md`.

## Local development

One command, no accounts, no cluster access, no container engine:

```sh
aspect mythra dev up
```

Then open http://127.0.0.1:4254 and sign in as any name. The stack runs in
the background; drive it with:

```sh
aspect mythra dev status              # per-leg health, non-zero if unhealthy
aspect mythra dev logs                # recent lines from every leg
aspect mythra dev logs --leg=mythrad  # follow one leg: pg|ch|otelcol|flagd|ingest|devissuer|mythrad|web
aspect mythra dev smoke               # prove the stack end to end: a headless player connects and its telemetry lands
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
| dev OIDC issuer | 127.0.0.1:9635/realms/dev | Keycloak-shaped; signs any subject; mythrad validates it through the same `oidcGate` path as prod |
| mythrad | 127.0.0.1:9634 (HTTP), :4433 (WebTransport) | self-signed cert pinned via `certHashB64`; wasm modules hot-load from `src/services/mythrad/behaviors/` |
| web app | http://127.0.0.1:4254 | vite dev server, proxying `/session` `/wt-info` `/terrain` `/behavior` `/assets` to mythrad and `/api/events` to the ingest exactly as the prod Ingress does |

Every telemetry lane a change emits is queryable locally: the `up` card
prints a copy-pasteable ClickHouse query (`guardian_analytics.events` for
product events, `guardian_analytics.otel_traces` for spans) and the
committed flags.json path for flag flips.

Nothing here bypasses production code paths: admission, module
distribution, terrain serving, and the netcode all run the same code they
run in the cluster. The only dev-specific piece is the issuer, and mythrad
only trusts it because `OIDC_ISSUER` says so.

## The edit loops

- **Web (TypeScript/React)**: save; vite HMR applies it. The web app lives
  in the vite-plus workspace (`src/products/viteplus-monorepo/`, see its
  `README.md` for the workspace commands).
- **Sim (Rust → wasm)**: `bazelisk run //src/services/mythrad/sim:refresh`
  while the stack runs. The new module lands in the behavior dir, mythrad's
  2s poll hot-swaps it, and connected clients follow the same update lane a
  production deploy uses.
- **mythrad (Go)**: `aspect mythra dev down && aspect mythra dev up`;
  clients redial and resync (also a prod-truthful path).

## Load bots

The dev issuer honors the `client_credentials` grant for any client id, so
loadgen's real admission path (`azp=mythra-loadgen` + `?bot=<n>` subject
suffixes) works locally without secrets.

## Degradation harness

`netsim` is a dev-time UDP impairment proxy between the browser's
WebTransport dial and mythrad's QUIC listener. Chrome DevTools network
throttling does not touch QUIC, so this proxy is the only honest way to
degrade the game path. It simulates latency/jitter, packet loss, subway
tunnels (total silence), tower switches (path migration — the server
sees the connection arrive from a new source port), and interface
teardown (`/sever`). Jitter and loss draw from a seeded generator so
scenarios replay identically.

```sh
bazelisk run //src/services/mythrad/netsim &
WUM_DEV_PUBLIC_ADDR=127.0.0.1:14433 aspect mythra dev up   # dial via proxy
curl -X POST 'http://127.0.0.1:14434/impair?latency_ms=300&jitter_ms=40&loss_pct=5&seed=7'
curl -X POST 'http://127.0.0.1:14434/silence?ms=8000'
curl -X POST 'http://127.0.0.1:14434/migrate'
```

`apps/wake-up-mythra-web/e2e/degradation.mjs` drives a full scenario
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
aspect mythra logs --since=10m --tail=500
aspect mythra psql --query='SELECT * FROM park_events ORDER BY seq DESC LIMIT 10'
aspect mythra dump > mythra.sql
aspect mythra restart
```

`status` and `logs` use standing read authority. `psql` and `dump` derive
`guardian-mythra-observer`, which can exec only `psql`/`pg_dump` in a fixed,
network-confined console carrying PostgreSQL's native `mythra_readonly` role.
`restart` derives `guardian-mythra-operator`, which can only delete a labeled
`mythrad` pod; the Deployment recreates the Git-declared revision. Neither role
can read Kubernetes Secrets, exec into mythrad or the shared Postgres pods, or
touch payments/Directus. Scaling, image, behavior, and configuration changes
remain Git/Flux operations.

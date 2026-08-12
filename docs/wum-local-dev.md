# Wake Up Mythra: local development

One command, no accounts, no cluster access:

```sh
scripts/wum-dev.sh
```

Then open http://127.0.0.1:4254 and sign in as any name. The stack it
starts — and tears down together on Ctrl-C:

| piece | where | notes |
| --- | --- | --- |
| journal db | docker `wum-dev-pg`, 127.0.0.1:55432 | empty is complete: parks genesis themselves on first open |
| dev OIDC issuer | 127.0.0.1:9635/realms/dev | Keycloak-shaped; signs any subject; mythrad validates it through the same `oidcGate` path as prod |
| mythrad | 127.0.0.1:9634 (HTTP), :4433 (WebTransport) | self-signed cert pinned via `certHashB64`; wasm modules hot-load from `src/services/mythrad/behaviors/` |
| web app | http://127.0.0.1:4254 | vite dev server, proxying `/session` `/wt-info` `/terrain` `/behavior` `/assets` to mythrad exactly as the prod Ingress does |

Nothing here bypasses production code paths: admission, module
distribution, terrain serving, and the netcode all run the same code they
run in the cluster. The only dev-specific piece is the issuer, and mythrad
only trusts it because `OIDC_ISSUER` says so.

## The edit loops

- **Web (TypeScript/React)**: save; vite HMR applies it.
- **Sim (Rust → wasm)**: `bazelisk run //src/services/mythrad/sim:refresh`
  while the stack runs. The new module lands in the behavior dir, mythrad's
  2s poll hot-swaps it, and connected clients follow the same update lane a
  production deploy uses.
- **mythrad (Go)**: restart `scripts/wum-dev.sh`; clients redial and
  resync (also a prod-truthful path).

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
WUM_DEV_PUBLIC_ADDR=127.0.0.1:14433 scripts/wum-dev.sh   # dial via proxy
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
                                  # prod journal (needs cluster read access)
                                  # to replay real park history locally
```

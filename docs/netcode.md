# Wake Up Mythra netcode — journaled deterministic simulation

Status: plan of record (2026-08). This document is the contract for the
game's network, simulation, and persistence architecture. The companion
product/platform plan is docs/wake-up-mythra-development.md.

## The one-paragraph model

Every surface — server, browser, app, load bot — runs the identical
fixed-point wasm simulation. The server never streams state; it streams the
**journal**: an ordered log of events `{seq, tick, kind, payload}`, and every
replica derives identical state from it, verified by a world hash. The
journal is also the durable truth (Postgres), so replay, restore-from-backup,
rejoin, spectating, and time-travel debugging are all the same operation.
Steady-state traffic is zero bytes per tick: data moves when a human acts,
plus a tiny client-pulled hash check. Distributed-systems kin: a replicated
state machine (LMAX/Raft shape) whose log doubles as the wire protocol.

## Components and interfaces

The wasm module is the narrow waist. Go and TypeScript never interpret game
state or event payloads — they move opaque bytes between the wire, the
journal, and the sim. If a host language can see a game rule, the rule is in
the wrong place.

### Sim core (wasm) — identical exports on every host

    sim_set_terrain(blob) -> ok|err         load a terrain artifact (own buffer)
    sim_init(seed, park_id, epoch) -> ok|err  fresh park on the loaded terrain
    sim_restore(snapshot) -> ok|err         from a snapshot blob; refuses to
                                            restore against the wrong terrain
    sim_snapshot() -> bytes                 full state, canonical encoding
    sim_step()                              one tick, pure function of state
    sim_apply(event) -> ok|reject(code)     MUST NOT mutate on reject
    sim_hash() -> u64                       world hash (terrain id folded in)
    sim_tick() -> u64
    sim_epoch() -> u32                      module epoch compatibility
    sim_terrain_id() -> u64                 identity of the adopted terrain
    sim_view(out) -> len                    render data (positions/skins/moods)

`sim_apply` doubles as validation: the authority calls it, and reject means
the intent never becomes an event. There is no separate validate() to drift
out of sync. Transactionality (no mutation on reject) is what makes that
safe. Prediction needs no extra ABI: clients fork via snapshot/restore.

Hosts driving wasm from Go must truncate i32 results (`uint32(res[0])`):
wazero's raw result slots are unspecified above the result width and the
arm64 backend leaves argument remnants there.

**Movement is state, never cache.** A dog's movement is exactly
(position Q16.16, waypoint node, target node) — all hashed, all in the
snapshot. The waypoint is recomputed by deterministic A* only at state
transitions (new target, corner reached, blocked), so a restored snapshot
continues movement bit-identically; there is no path cache whose absence
could diverge a replay.

### Wire protocol (WebTransport; one transport for players and spectators)

Session setup over HTTPS, then one bidi reliable stream plus datagrams:

    POST /session      Authorization: Bearer <OIDC access token>
      -> { ticket, endpoint, cert_hash?, park_id, role }
      403 unless email_verified. ticket = HMAC{sub, park, role, exp~60s}.

    client -> server (stream):
      hello   { proto: 3, ticket, since_seq? }
      intent  { intent_id u64, kind u16, payload }     opaque payload
      resync  { have_seq }
    server -> client (stream, strictly ordered):
      welcome  { session, role, epoch, seq, tick, terrain }
      event    { seq, tick, kind, payload }            the journal, verbatim
      reject   { intent_id, reason }
      snapshot { seq, tick, epoch, wh, terrain, state }  deflate-compressed
    client -> server (datagram):  check   { tick, wh }
    server -> client (datagram):  verdict { tick_now, ok, cw, pw }
                                  cw/pw = current client/park module hashes

The terrain hex is the only terrain fact on the wire — dimensions and
schema live in the content-addressed blob every consumer fetches.

There is no ack message: an accepted intent returns as an `event` carrying
its `intent_id` — the journal is the acknowledgment. Spectators are sessions
whose role rejects write intents at ingress; same protocol, same sim.
Intents are idempotent by `(actor, intent_id)` within a generous window, so
clients resend pending intents after any rejoin.

### Journal (Postgres behind a backend-agnostic contract)

    park_events   (park_id, seq, tick, epoch, kind, actor, intent_id,
                   payload bytea, wall_ts, PK (park_id, seq))
    park_snapshots(park_id, seq, tick, epoch, wh, terrain_id, state bytea,
                   wall_ts, PK (park_id, seq))
    park_terrain  (terrain_id, schema, blob bytea, wall_ts, PK (terrain_id))

    type Journal interface {
        Append(ctx, parkID, []Event) (firstSeq, err)   // sole writer = authority
        Read(ctx, parkID, fromSeq) (iter, err)
        PutSnapshot(ctx, parkID, Snapshot) error
        LatestSnapshot(ctx, parkID) (Snapshot, error)
        PutTerrain(ctx, terrainID, schema, blob) error // immutable per id
        TerrainBlob(ctx, terrainID) (blob, ok, error)
    }

Contract semantics live in the interface, not the backend: per-park seq is
dense, gap-free, monotonic, assigned at append; single-writer per park is
enforced (a split-brain writer conflicts, never interleaves); payloads and
snapshots are opaque bytes end to end. `journaltest.Run(t, impl)` is the
conformance suite any implementation must pass; `wall_ts` is observability
only and never replayed.

**Write batching.** Appends run once per server tick, batched across all
parks on the hub: intents accepted during tick T are applied to their parks'
authorities and staged; at the tick boundary one multi-row transaction
appends every staged event; fan-out happens only after commit. At most 24
commits/second per hub regardless of park count; a tick with no events
writes nothing.

**Durable-before-visible (invariant).** No event reaches any session before
its journal append commits. Fanning out first and crashing before commit
would leave every client applying an event the restore path has never heard
of — permanent divergence. This ordering costs up to one tick (~42ms) of
intent latency and is not negotiable.

### System actors — one door for everything that is not a player

The scheduler (day resets, check-in windows, mood phases; in-process — it
owns the park clock), the entitlements/Crystals settlement, and the
end-of-month ClickHouse ranking batch are privileged intent producers using
the same path: sim_apply on the authority → append → fan out. External
producers reach it via `POST /internal/parks/{id}/events` (authenticated,
network-policied). Nothing else may write. This is also the money boundary:
purchases settle outside the sim (TigerBeetle/entitlements); only settled
consequences become journal events. Wall clocks enter the sim only as
server-issued events — client clocks are never authoritative.

### Module epochs

The ConfigMap hot-swap lane ships module bytes; an `epoch_advance{epoch,
module_hash}` journal event makes a swap effective (module_hash = first 8
LE bytes of the module's sha256). The authority runs the whole lane:

1. **Soak.** A new park module on the mount becomes a candidate instance
   restored from the live snapshot and run in the dark — fed every event
   the live module accepts, stepped in lockstep, fanning out nothing — for
   ~5s. A trap, a reject the live module didn't issue, or an instantiation
   failure pins the bytes as bad until the mount serves different ones.
2. **Commit.** The boundary is journaled: `epoch_advance` leads a normal
   tick batch (durable before visible, like every event). Immediately
   after that tick, the candidate re-restores the authoritative boundary
   state and a **synchronous** snapshot goes durable with the world hash
   computed by the NEW module — so an epoch boundary is always a snapshot
   boundary, and replay never crosses a module change: `openAuthority`
   restores from that snapshot under the converged module. Only then does
   the candidate become the host. Its soak state is discarded — it was a
   validation instrument, not a lineage.
3. **Clients follow the event.** Applying `epoch_advance` (or seeing a
   verdict whose park-module hash disagrees — the backstop for a replica
   attached mid-boundary) fetches /behavior by hash into a background
   instance and requests a resync; the snapshot restores there and the
   instances swap between frames. The post-restore hash check vouches for
   the whole move. Load bots follow identically.

A hash-compatible module (refactor, dead code) swaps with byte-identical
snapshots and an unchanged world hash; a behavioral module re-anchors the
hash lineage at the boundary. Either way pre-boundary history replays
under snapshots recorded at-or-after the last boundary, so old journals
never meet new rules.

### Terrain artifacts

A park's world is durable data, not a function of code: the terrain blob is
generated once (fixture today, procgen later), stored content-addressed in
`park_terrain`, and served immutably at `/terrain/<16-hex>`. The identity is
`mix64(fnv1a(blob))` — computable inside the no_std sim, so one name covers
the journal payload, the URL, and the world hash.

The lane mirrors module epochs exactly. Hosts load the blob through
`sim_set_terrain` before `sim_init`/`sim_restore`; a change to a live park
is a `terrain_set{schema, terrain_id}` journal event, and the host (server
replay and browser alike) fetches + loads the blob *before* feeding the
event — the sim refuses to adopt an identity the loaded blob doesn't carry,
and refuses to restore a snapshot taken on different terrain, so the
load-first contract cannot rot silently. On adoption the sim deterministically
relocates any dog left standing on a now-invalid node and clears every
movement intent. Schema migrations are backfills: a deterministic migrator
producing new blobs plus one `terrain_set` per park, never an in-place edit.

## Client netcode

**Tick discipline.** The client steps its replica off its own clock at the
sim tick rate (a sim constant, compiled in — not a deployment knob),
disciplined against the server: each `verdict` carries the authority's
current tick and yields an RTT sample; the client targets server-now minus
6 ticks (250ms) for its authoritative replica and corrects by slewing tick
duration ±2%, never by jumping.

**Late events (rollback).** The client keeps an in-memory ring of replica
snapshots (one per second, 10 deep). An event for a tick already stepped
restores the newest snapshot ≤ its tick and replays forward. The stream is
ordered, so only lateness — never reordering — occurs.

**Prediction.** The renderer draws a predicted fork: authoritative state
plus pending own intents, rebuilt whenever either changes. A matching event
retires the intent (usually a visual no-op); a reject rebuilds and surfaces
the reason. Only the local player's own intents are ever predicted.

**Hash checks (client-pulled).** The client polls `check{tick, wh}` against
its own recent-hash ring; the server answers from a per-park hash ring
(~30s). Poll interval is pure client behavior driven by the
`netcode-check-seconds` flag (sticky per OIDC sub) — cellular backs off,
hidden tabs stop entirely, wifi polls eagerly. A `check` older than the
server ring returns unknown, which counts as a strike.

**Resync.** Two consecutive failed checks trigger it (one can be a rollback
racing the check). Divergence always transfers a fresh on-demand snapshot —
replaying events onto corrupt state cannot repair it. The client buffers the
live stream, restores into a background instance, applies buffered events,
steps headless to target, swaps between frames, rebases predictions, then
fires an immediate check to confirm. Two consecutive failed resyncs mean the
module, not the state, is suspect: re-fetch by epoch and re-enter.

**Catch-up (join/rejoin).** One machine, three entrances: fresh join gets
snapshot + events since; rejoin (`since_seq`) gets whichever of
events-since / snapshot is fewer bytes — except a tick gap over ~30s forces
the snapshot (fast-forward compute is a battery cost too); divergence always
gets the snapshot. The client has no policy; it applies what arrives.

**Corrections, visually.** Position corrections reachable by capped-velocity
walking within ~600ms are walked; larger gaps crossfade over ~300ms.
Discrete state (balances, packs, phases) snaps instantly — a number
animating toward the truth reads as a glitch, not polish.

## Admission and identity

`POST /session` sits behind the Cloudflare-proxied ingress and verifies the
OIDC access token (issuer JWKS, `email_verified` required). The HMAC ticket
it mints carries identity, park, role, and expiry into the QUIC hello —
admission control, authentication, and read-only enforcement in one
artifact. Anonymous spectating is deliberately deferred; when it returns it
is a ticket minted without identity, same transport. Unticketed hellos are
rejected before any session state is allocated; QUIC Retry and per-source
handshake limits bound the pre-ticket surface. Sessions are capped and
flow-control windows sized so the cap fits in memory with headroom
(GOMEMLIMIT as the backstop).

## Observability and targets

- Steady-state downlink per *idle* session ≤ ~100KB/hour (checks + verdicts);
  the cellular-usage promise is the headline product metric. This holds for
  a quiet or realistically-sized park. It does **not** hold for a park with
  thousands of *simultaneously active* players: events fan out to every
  session, so downlink scales with total human activity × occupancy. A
  2000-active-player park measured ~20MB/hour/session — still far under the
  old per-tick streaming cost (87MB/hour regardless of activity), and still
  bounded by *human* action rate, but the mega-park case needs interest
  management (spatial/rate culling of the event fan-out) before it meets the
  idle target. Tracked as future work, not a regression.
- Tick p99 within the tick budget; batch-commit p99 under one tick.
- Divergence rate ~0; every resync is exported with a reason label.
- Social events (intent → visible on every session) p99 ≤ 2s.
- Restore drill: snapshot + replay reproduces the world hash exactly;
  a park whose replay hash mismatches is refused service, never served
  wrong.

### Measured — 2000-session single-park run (prod, 2026-08)

Every session an OIDC-ticketed journal session through the real admission
path; bots acting at human rate.

| Metric | Result | Budget / prior |
|---|---|---|
| Sessions held | 2000 / 2000 | breaker never opened |
| Authority CPU | 1.34 cores | — |
| Authority RSS | 702 MB, flat (max 704) | **no OOM** (old arch OOMKilled at 1Gi) |
| Tick p99 | 9.9 ms | 41.7 ms budget |
| Journal commit p99 | 9.3 ms | < one tick |
| Journal append errors | 0 | — |
| Datagram send errors | 0 | (the #1328 signature) |
| Pod restarts | 0 | — |
| Intent → visible p99 | 74 ms | 2 s SLA |

The memory wall that OOMKilled the previous architecture at 2000 sessions is
gone: QUIC flow-control window caps + GOMEMLIMIT hold RSS flat because there
is no per-session state firehose to buffer. The circuit breaker in the load
driver also proved out — at authority-kill it absorbed the 200-session
reconnect storm with a 30s pause instead of the retry-amplified crashloop
the previous driver caused.

Time-travel debugging and balance regression: any journal prefix replays
into a state whose hash must match the recorded snapshots; proposed balance
changes replay historical journals in the shadow lane before promotion.

## Restore drill (runbook)

The claim "weeks of accumulated progress survive any failure" is proven, not
asserted, by killing the authority mid-load and watching parks reopen from
the journal:

1. Start a load run against a park (`loadgen`, or real sessions). Note the
   journal head: `mythra_journal_events_total` climbing, `mythra_snapshots_total`
   incrementing every ~512 events.
2. Kill the pod: `kubectl -n tenant-guardian-prod delete pod -l app.kubernetes.io/component=mythrad`.
   The Deployment (maxSurge 0) reschedules on `ash-earth`; sessions see the
   QUIC connection drop and redial.
3. On reopen, `openAuthority` restores the latest snapshot and replays the
   journal tail. The invariant: `sim_restore` must reproduce the snapshot's
   stored world hash, and replay must not reject any event. A mismatch logs
   `snapshot hash mismatch ... refusing to serve` and the park stays closed
   — the roll-forward doctrine (never serve divergence). Watch for it in the
   pod logs; its absence is the pass.
4. Every redialing client sends `since_seq`; the authority answers with
   min-cost catch-up. Clients whose `since_seq` is fresh replay a handful of
   events; the rest get a fresh snapshot. `mythra_catchup_total` splits the
   two.
5. Confirmation: `mythra_checks_total{result="mismatch"}` stays flat through
   the recovery (every replica reconverged to the authority's hash), and the
   journal head continues past where it was before the kill (no events lost —
   durable-before-visible guaranteed nothing fanned out that wasn't
   committed).

Recovery time is snapshot-restore (milliseconds) + tail replay (thousands of
ticks/second) + client redial backoff; a park minutes deep in its journal
reopens in under a second of authority time. The bound on tail length is the
snapshot cadence, not the age of the park.

Backup and PITR ride the shared products Postgres: continuous WAL archiving
plus the nightly base backup (docs/reliability-rto.md). A cluster-loss
restore rebuilds `park_events`/`park_snapshots` to the RPO, and every park
reopens by the same replay path — the journal is ordinary rows, so it
inherits the database's disaster-recovery story for free.

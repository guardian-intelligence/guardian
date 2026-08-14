# Wake Up Mythra netcode

"Wake Up, Mythra!" uses a shared server-authoritative deterministic simulation with periodic reconciliation. Clients send inputs which are logged to a journal and broadcast to all connected clients. Product/platform plan: docs/wake-up-mythra-development.md.

## Goals

Every surface — server, browser — runs the identical fixed-point
wasm simulation. The server never streams world state; it streams an ordered
list of **events** (the journal), and every replica derives the same world
from it. The design optimizes for four things, in order: **correctness you
can prove** (a 64-bit world hash makes divergence detectable, snapshots make
it repairable), **surviving any restart** (the journal is Postgres rows, so
recovery is the database's problem — a solved one), **cellular-class
bandwidth** (an idle session costs ~zero bytes/tick; data moves when a human
acts), and **shipping game rules to a live world** (code swaps ride the same
event stream as everything else).

## Guiding principles

1. **Replicate events, not state.** The server appends events to a list; the list's order — not wall-clock time — is the law. Every replica applies
   tick T's events in list order, then simulates tick T. There are no
   timestamps inside the sim, so "simultaneous" events cannot diverge.
2. **Durable before visible.** An event is written to Postgres before it is
   applied anywhere — including the server's own world. Flipped, a crash
   between fan-out and write would leave clients having applied an event
   that replay has never heard of: permanent divergence.
3. **The hash check answers one question** — "is your state at tick T
   bit-identical to mine?" — and only for T inside the server's ~30s hash
   ring. It is structurally blind to a client that is *correct but stale*;
   liveness belongs to the clock (`sim/clock`), correctness to the checks.
4. **Replaying events onto corrupt state cannot repair it.** Replay is a function of (base state, events); a wrong base plus right events is still wrong. Client divergence is always repaired with a snapshot.
5. **A code swap and a snapshot happen at the same tick, atomically.** So restart-replay never runs any event through the wrong version of the rules, and old journals never meet new rules.

## Modules

| Where | What | Owns |
|---|---|---|
| `sim/park` (wasm, no_std) | the game rules | all state, validation (`sim_apply` rejects without mutating), hashing, snapshots |
| `sim/nav` | deterministic A* | pathing, `path_cost`; movement is state (position, waypoint, target — all hashed), never a cache |
| `sim/clock` (wasm, no_std) | client tick discipline | Acquiring → Locked (±2% slew) → FastForward (big deficits) → SnapshotRequired (beyond the ring). No floats, no host clocks — the host feeds it times and executes its step-count directives |
| `sim/session` (wasm, no_std) | the replica session | the wire codec, seq-dense event ordering, snapshot ring + rollback, resync/strike policy, intent identity + resend, and the own-intent prediction overlay over two host-held park slots (journal replica / presented). Time and transport are inputs; the host executes its verbs |
| `sim/client` (wasm) | presentation + the session ABI | render smoothing; re-exports the clock and the session. Never feeds back into world state |
| `mythrad/park.go` | the authority | the anchored tick schedule, event stamping, journal append, hash ring, snapshot cadence, the module-swap lane |
| `mythrad/session.go` | transport | WebTransport sessions, OIDC-ticket admission, intent→actor binding, fan-out |
| `mythrad/journal` | durability | Postgres `park_events` / `park_snapshots` / `park_terrain`; per-park seq is dense and single-writer; `journaltest.Run` is the conformance suite |
| `packages/mythrad-client-core` | the host | moves opaque bytes between wire, wasm, and screen: two park slots, the client module, the transport, and the read surface a renderer and HUD consume. If TypeScript (or Go) can read a game rule, the rule is in the wrong place |
| `apps/wake-up-mythra-web/src/game` | the surface | platform adapters (WebTransport, fetch, auth), the isometric renderer, the HUD, and the telemetry mapping. No protocol |

## Interfaces

Sim ABI (identical on every host): `sim_set_terrain`, `sim_init`,
`sim_restore` (refuses wrong terrain), `sim_snapshot`, `sim_step`,
`sim_apply` (validation and application in one — no separate `validate()` to
drift), `sim_hash`, `sim_tick`, `sim_epoch`, `sim_rate` /
`sim_anchor_tick` / `sim_anchor_ns` (the rate segment), `sim_view`, and
`sim_hud` (the 28-byte HUD projection keyed on the viewer's dog). Go hosts must
truncate i32 results (`uint32(res[0])`) — wazero leaves garbage in the high
bits on arm64.

Wire (WebTransport; players and spectators share it): `POST /session` with an
OIDC bearer mints a short-lived HMAC ticket → one ordered stream of binary
frames — QUIC-varint length, kind byte, little-endian fields (proto 4;
`mythrad/wire` and `sim/session` are the two codecs, pinned to shared golden
bytes) — carrying `hello`, `intent` / `welcome{..., hz}`, `event`, `reject`,
`snapshot`, plus fixed-layout datagrams (`check{tick, wh, ct}` /
`verdict{tick, now, ct, flags, cw, pw}`). There is no ack: an accepted intent
comes back as an `event` carrying its intent id — the journal is the
acknowledgment, and the id (a per-connection nonce over a counter) is the
only handle that marks an event as yours. Rejects go only to the sender.

## Decisions (and why)

- **Validation is `sim_apply` itself.** Reject means no mutation, so the
  authority can probe with the real function; no parallel validator to rot.
- **The clock is a no_std wasm crate.** Purity by construction: it cannot
  reach `Date.now()` or `navigator`, so it cannot smuggle nondeterminism.
- **Code swaps soak in the dark, then discard their state.** A candidate
  module runs ~5s beside the live one, fed the same events, fanning out
  nothing. At commit the live world is copied in at the boundary tick,
  snapshotted by the *new* module, then swapped. The soak state is a smoke
  test, not history — players' inputs never retroactively change meaning.
- **Clients follow swaps via the event stream**: apply `epoch_advance`,
  fetch the module by hash, restore the boundary snapshot into a background
  instance, swap between frames. The verdict's module hash (`pw`) is the
  backstop for anyone who missed the event.
- **Terrain is content-addressed data, not code.** Blob in Postgres, served
  immutably by hash; the sim refuses a snapshot taken on different terrain.
- **One door for privileged writers.** Scheduler, settlements, batch jobs —
  all produce intents through the same apply→append→fan-out path as players.
  Money settles outside the sim; only settled consequences become events.
- **Snapshots are not compaction.** A snapshot is a resync payload and a
  replay floor. Deleting journal rows behind a *verified* snapshot is a
  separate future job ("journal retention") — see the FAQ.
- **A tick number is a timestamp.** The sim's state carries a rate segment
  `(rate_hz, anchor_tick, anchor_ns)`: tick `anchor_tick` falls at
  `wallEpoch + anchor_ns` (plus a per-park phase offset derived from the
  immutable park id — voteable metadata can never move a park's clock, and
  co-located parks stagger their tick work for free), and later ticks
  advance at `rate_hz`. The scheduler chases that definition instead of
  free-counting ticks, so hitches and downtime are always repaid and drift
  cannot accumulate — real-world mechanics (harvests, meetups, check-in
  windows) compile to plain tick arithmetic inside the clock-free sim.
  `mythra_tick_lag_seconds` measures the server against its own schedule.
- **The tick rate is world state, not deployment config.** `TICK_HZ` only
  expresses the *desired* rate: a park whose journaled rate differs
  converges via one `rate_set` event in the dark phase (reopen), which
  re-anchors the mapping piecewise — so raising, lowering, or rolling back
  a rate never stalls or forks a schedule, and every pod generation
  derives the same one from the journal. Clients pace from `welcome.hz`;
  a connection sees exactly one rate. (What changes with rate: game
  tuning is tick-denominated — wander odds, soak windows — so a rate
  change is also a balance change until constants are wall-derived.)

## Architecture invariants

1. **Server-authoritative simulation.** The server's world state is the only
   truth. Clients never mutate world state; they send intents, the sim
   applies them. Anything purchasable or rankable (Energy, Coin, Favor, Fur,
   Crystals, prizes, check-ins) is computed server-side without exception.

2. **Journaled deterministic simulation.** Every surface runs the identical
   sim; the server streams an ordered event journal, never state, and the
   journal is also the durable truth (Postgres) — replay, restore, rejoin,
   and spectating are the same operation. Clients run no prediction: one
   world per client, built from the journal, with instant cosmetic tap
   feedback only; divergence is detected by client-pulled
   world-hash checks and repaired by snapshot resync. The full contract —
   wire protocol, batching, epochs, catch-up, corrections — is the rest of
   this document. Steady-state downlink for an idle session is a few
   bytes of hash checks; the cellular-usage promise is a headline product
   metric.

3. **Freshness target: within one tick of the authority.** The replica
   aims to trail the authority's present by no more than one tick
   (`TRAIL_TARGET_TICKS`, clock crate) — latency is solved by server
   placement, not client complexity. The operating cushion (`LAG_TICKS`)
   is currently wider, sized to absorb delivery jitter; every reduction
   toward the target must show up in the measured trail, which rides the
   session diagnostics record (`session_diag`) and is visible live in
   prod via the in-game Stats for Nerds pane (`?stats=1` or backquote).
   Player intents apply at the first authority tick after receipt and are
   idempotent by `(actor, intent id)` across reconnects; the client
   measures each action's first-wire-write→applied latency in the session
   core and reports it as a finished fact (`answered`), so every host on
   every platform grades feel with the same number.

4. **One deterministic core, unchanged, on every surface.** Game logic
   compiles from the shared Rust structural core (`//src/services/mythrad/sim`)
   to wasm. The module bytes are the portability contract. Three rules keep
   the determinism absolute:
   - **Fixed-point only.** The sim is integer arithmetic throughout
     (fractional values in Q16.16); float types are banned from the wasm
     modules and enforced at build time twice — a source token gate
     (`sim:no_float_test`) and a wasm binary scan for float value-type
     declarations (`mythrad_test`). No FPU, rounding mode, or NaN payload
     on any surface can ever matter.
   - **Shared randomness seed.** Each dog park gets a server-minted seed,
     broadcast in `welcome`/`presence`; every roll is `det_rand(seed, tick,
     entity)` — a pure function — so any surface holding the seed
     reproduces the server's dice exactly. Time sync is a non-issue: a
     single pod owns each park's simulation and its tick counter.
   - **The `world_hash` oracle.** The core exports an order-independent
     world-state hash; the server stamps it on one tick per second, and
     every client re-derives it from its own snapshot through the client
     module and displays ✓/✗. This is the cross-surface determinism
     assertion the QA harness scripts against.
   Per surface, only the embedding varies: wazero (server, compiled),
   browser WebAssembly (web, JIT), interpreter → app-store AOT (iOS/iPadOS
   app), WebView or JNI runtime (Android app). If a surface cannot run the
   identical bytes, the design is wrong, not the surface.

5. **Seamless updates: the ladder.** Every layer updates live, in order of
   blast radius, and the running session survives all of them:
   - assets: content-addressed, streamed on first reference;
   - server behavior: dark launch (shadow slot evaluated every tick on live
     inputs, divergence exported as metrics, world untouched) → switch flip
     (promotion moves shadow bytes into the live slot; connected clients see
     the hash flip mid-session);
   - client presentation module: hash rides every pong; a flip hot-swaps the
     module mid-session on web. iOS app lane: OTA updates run interpreted,
     with an in-game indicator to update the app for the AOT build (gated by
     app-store update checks);
   - server binary: image roll (sessions rejoin by journal catch-up);
   - network/routing: no coordination — see invariant 5.

6. **Clients auto-reconnect seamlessly.** Severed connections are
   unavoidable, so any change to the network or routing layer may sever them
   without ceremony: every client redials with backoff and rejoins by
   journal catch-up (`since_seq`), and the server sends whichever of
   missed-events or snapshot is cheaper and the replica converges. An involuntary disconnect is invisible
   to game semantics — pack membership and park presence live in the
   journal, not in the connection.

7. **Feature flags are presentation and product gating — never sim inputs.**
   Client-side flagging uses the OpenFeature SDK evaluating over OFREP
   against the same-origin /features mount, with a read-only SSE
   subscription to the flag-set epoch so flips propagate live without
   reload (docs/feature-flags.md). The hard rule: a flag must never
   influence a behavior module's step function or any server-side resource
   computation — sim changes go through the dark-launch/promotion ladder
   where divergence is measured, not through flags. Flags gate UI, features,
   rollout cohorts, and kill switches.

- **A replica pays one invisible rollback per own action, priced at its
  lead.** The authority stamps an intent at the tick IT holds, so a
  replica running ahead of the authority — which any freshly restored
  replica is, and any deliberately fresh one will be — receives its own
  action stamped in its past and repairs it by rewind-and-replay inside
  a single frame. Non-acting dogs replay bit-identically (the sim has no
  dog-dog interaction; revisit this economy when it does), the presented
  tick never regresses, and the repair's two costs are CPU (bounded by
  the pump budget, resync as the escape) and nothing visible. Freshness
  and rewind depth are the same dial; in-frame replay is what makes
  turning it affordable.
- **An unanswerable hash check is only evidence of staleness in one
  direction.** The verdict carries the authority's own tick: a check the
  park cannot answer because the replica asked about the park's near
  future — the normal state of a leading replica — is benign and never
  strikes; only a check outside the ring's past counts toward the
  two-strike resync. Without this distinction a healthy, merely-ahead
  client resyncs itself on a perfect link, and the fresher the client
  runs, the more often.

## FAQ

**What actually happens on pod restart?** Restore the latest snapshot,
replay events after it, refuse to serve if the replayed hash mismatches the
recorded one (never serve divergence), then repay the downtime before the
doors open: a gap under a minute is stepped through (the world lived while
the server was away), a longer one journals a single `clock_skip` event that
jumps the tick to the schedule and re-floors the snapshot. Either way the
park reopens on exactly the tick the wall clock defines. Measured: a park
minutes deep reopens in under a second; drilled live three times with a
player connected.

**What if the wall clock itself jumps?** Forward: the schedule demands
catch-up ticks, bounded per wakeup; past the hash ring the park closes and
repays the gap through the dark reopen path. Backward (NTP step, lying
RTC): the park never unticks and never idles backward — it simply waits for
reality to catch up, visible as negative `mythra_tick_lag_seconds`.

**A client sends an intent during a server blip — then what?** Today: lost.
Intents are idempotent by `(actor, intent_id)`, so client-side
resend-after-rejoin is safe by design but not yet implemented. Backlog.

**Two players act "at the same instant" — who wins?** Whoever the authority
appends first. Arrival order at the single writer is the tiebreak; no
replica ever consults a clock to decide.

**Why does the journal grow forever?** Because replay-from-genesis is the
most valuable debugging tool a young deterministic sim has, and deletion is
the one operation roll-forward can't fix. Retention becomes worth building
when storage cost beats that; its precondition is deleting only behind a
snapshot verified to restore bit-identically, keeping a few older snapshots
as forensic anchors.

**Storage math (exercise).** A journal row is ~120B (fields + payload +
tuple overhead); a snapshot every 512 events. Small park, 20 players × 2
acts/min: 57,600 events/day ≈ 7MB + ~112 snapshots ≈ 15MB/day — years per
10GB. The 2000-bot drill measured 1–2GB/day: scale is linear in *human*
action rate, and ticks cost zero. Rerun the math before believing any
retention urgency.

**What does an idle session cost?** ~100KB/hour downlink (checks +
verdicts). An *active* park scales with total human activity × occupancy —
the 2000-active drill measured ~20MB/hour/session. A mega-park needs
interest management (culling the fan-out) before it meets the idle target;
tracked as future work.

**How fast is it?** 2000 real ticketed sessions, one park, prod: tick p99
9.9ms (41.7ms budget), journal commit p99 9.3ms, intent→visible p99 74ms,
RSS flat at 702MB, zero restarts (2026-08 drill; the previous
state-streaming architecture OOMKilled at this session count).

**How does a stale-but-correct client recover?** The clock measures its
deficit against verdict ticks: small → slew ±2%, seconds → fast-forward
(budgeted extra steps per frame), beyond the ~30s ring → demand a snapshot.
One state machine, property-tested in `sim/clock`; the dev debug panel and
the netsim proxy (`docs/wum-local-dev.md`) exist to torture it.

# Wake Up Mythra netcode

Status: contract (2026-08). Product/platform plan: docs/wake-up-mythra-development.md.

## Goals

Every surface — server, browser, load bot — runs the identical fixed-point
wasm simulation. The server never streams world state; it streams an ordered
list of **events** (the journal), and every replica derives the same world
from it. The design optimizes for four things, in order: **correctness you
can prove** (a 64-bit world hash makes divergence detectable, snapshots make
it repairable), **surviving any restart** (the journal is Postgres rows, so
recovery is the database's problem — a solved one), **cellular-class
bandwidth** (an idle session costs ~zero bytes/tick; data moves when a human
acts), and **shipping game rules to a live world** (code swaps ride the same
event stream as everything else).

## The five laws

1. **Replicate events, not state.** The server appends events to a list; the
   list's order — not wall-clock time — is the law. Every replica applies
   tick T's events in list order, then simulates tick T. There are no
   timestamps inside the sim, so "simultaneous" events cannot diverge.
2. **Durable before visible.** An event is written to Postgres before it is
   applied anywhere — including the server's own world. Flipped, a crash
   between fan-out and write would leave clients having applied an event
   that replay has never heard of: permanent divergence.
3. **The hash check answers exactly one question** — "is your state at tick T
   bit-identical to mine?" — and only for T inside the server's ~30s hash
   ring. It is structurally blind to a client that is *correct but stale*;
   liveness belongs to the clock (`sim/clock`), correctness to the checks.
4. **Replaying events onto corrupt state cannot repair it.** Replay is a
   function of (base state, events); a wrong base plus right events is still
   wrong. Divergence is always repaired with a snapshot.
5. **A code swap and a snapshot happen at the same tick, atomically.** So
   restart-replay never runs any event through the wrong version of the
   rules, and old journals never meet new rules.

## Modules

| Where | What | Owns |
|---|---|---|
| `sim/park` (wasm, no_std) | the game rules | all state, validation (`sim_apply` rejects without mutating), hashing, snapshots |
| `sim/nav` | deterministic A* | pathing, `path_cost`; movement is state (position, waypoint, target — all hashed), never a cache |
| `sim/clock` (wasm, no_std) | client tick discipline | Acquiring → Locked (±2% slew) → FastForward (big deficits) → SnapshotRequired (beyond the ring). No floats, no host clocks — the host feeds it times and executes its step-count directives |
| `sim/client` (wasm) | presentation | render smoothing; re-exports the clock. Never feeds back into world state |
| `mythrad/park.go` | the authority | the anchored tick schedule, event stamping, journal append, hash ring, snapshot cadence, the module-swap lane |
| `mythrad/session.go` | transport | WebTransport sessions, OIDC-ticket admission, intent→actor binding, fan-out |
| `mythrad/journal` | durability | Postgres `park_events` / `park_snapshots` / `park_terrain`; per-park seq is dense and single-writer; `journaltest.Run` is the conformance suite |
| web `client.ts` | the host | moves opaque bytes between wire, wasm, and screen. If TypeScript (or Go) can read a game rule, the rule is in the wrong place |

## Interfaces

Sim ABI (identical on every host): `sim_set_terrain`, `sim_init`,
`sim_restore` (refuses wrong terrain), `sim_snapshot`, `sim_step`,
`sim_apply` (validation and application in one — no separate `validate()` to
drift), `sim_hash`, `sim_tick`, `sim_epoch`, `sim_view`. Go hosts must
truncate i32 results (`uint32(res[0])`) — wazero leaves garbage in the high
bits on arm64.

Wire (WebTransport; players and spectators share it): `POST /session` with an
OIDC bearer mints a short-lived HMAC ticket → one ordered stream (`hello`,
`intent` / `welcome{..., hz}`, `event`, `reject`, `snapshot`) plus datagrams
(`check{tick, wh}` / `verdict{tick_now, ok, cw, pw}`). There is no ack: an
accepted intent comes back as an `event` carrying its `intent_id` — the
journal is the acknowledgment. Rejects go only to the sender.

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

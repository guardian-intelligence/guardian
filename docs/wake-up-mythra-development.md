# Wake Up Mythra — development plan of record

Status: plan of record (2026-08). The presence plane, wasm behavior stack,
and update ladder described here are live at wakeupmythra.com; everything in
[Gaps and sequencing](#gaps-and-sequencing) is not.

## Work backwards from the surfaces

Every design decision starts from what we ship to, not from what is
convenient to build. The supported surface matrix is the contract; a change
that cannot be verified on a listed surface is not done.

### Browsers (explicit list — nothing else is "supported")

| Platform | Browsers | Engine reality |
|---|---|---|
| iOS / iPadOS | Safari, Chrome for iOS, Firefox for iOS, Edge for iOS | All WebKit wrappers today; one engine, one bug surface. Watch item: EU DMA alternative engines. |
| Desktop | Chrome, Firefox, Safari (macOS) | Three real engines. |
| Android | Chrome, Samsung Internet, Firefox for Android | Two engines (Blink ×2 — Samsung Internet trails Chrome by ~2 majors — plus Gecko). |

Backwards compatibility: for each browser, versions released up to **one
year** before current. Opera, Brave, Arc, and other derivatives will usually
work (Chromium) but are explicitly untested and unsupported — we do not hold
releases for them.

### Platforms and stores

- macOS App Store
- iOS App Store (own QUIC stack + interpreted-then-AOT wasm lane — see
  update ladder)
- Google Play Store
- Steam (tentative — do not spend on it until it graduates)
- Game Center (Apple surfaces) and Google Play Games (Android) for identity,
  leaderboards, achievements. Both bridge into the Guardian customer
  identity realm; neither becomes a second identity system.

### Minimum device targets (the floor we test on)

The low-end rack device is the release gate, not the median device.

| Dimension | Floor |
|---|---|
| CPU | 4× Cortex-A53 @ 1.4GHz class (2019 Android Go tier) |
| Memory | 2GB device RAM; game tab/app ≤ 150MB resident |
| Storage | Web: ≤ 25MB cached (shell + modules + assets). App: ≤ 200MB installed |
| Network | Playable at 1Mbps / 300ms RTT / 2% loss; steady-state ≤ 20KB/s down |
| Rendering | 60fps target, 30fps floor without sim divergence (presentation-only degradation) |

### Per-surface release gates

Every supported surface has e2e automation that gates the full release of
the components that reach it. One command (`//qa:surfaces`, see QA section)
runs the same scripted session — join → spectate → behavior hot-flip →
reconnect/resume — against every surface and asserts the shared oracle
(below). CI tier: browser engines + simulators, deterministic, per-PR.
Physical tier: the device rack, nightly and pre-release. A component's
release is gated only by the surfaces it ships to.

## The game (shared vocabulary)

Month cycle: **22 days asleep, 6 days awake**. Trainers accumulate Energy to
wake Mythra by month-end; success pays end-of-month prizes commensurate with
contribution, and awake-Mythra offers a limited-time bonus. Failure resets
the cycle.

Glossary:

- **Mythra** — baby samoyed onboarding sprite; asleep in a dog-house tile;
  communicates via thought bubbles; sleepwalks when the plot needs it.
- **Trainer** — human user; one Dog to start.
- **Dog** — has equipment slots and time-based pseudo-random **moods** that
  influence bonuses. Each dog gets a pack.
- **Pack** — up to 4 dogs a trainer manages for synergies. Passive bonuses
  accrue from pack composition. Leaving a dog park empties the leaver's pack
  and removes their dog from everyone else's.
- **Dog Park** — a room/world simulation tied by metadata to a real
  geographic dog park. Trainers declare one park home turf; their activity
  is associated with it. Location-change petitions are handled manually.
  (Deliberate tradeoff: multi-park regulars are underserved until we
  understand them; do not speculatively design for them.)
- **Check In** — available when physically present at the park's location.
  5 guaranteed minutes of resource collection; remaining present after that
  grants a bonus to every checked-in dog.

Resources:

| Resource | Role | Notes |
|---|---|---|
| Energy | Progress toward waking Mythra | Accumulates across the cycle; the shared goal |
| Coin | Upgrade dogs | Earned in play |
| Favor | Level up dogs | Earned in play |
| Fur | Limited-time item currency | Granted only by awake-Mythra; **rolls over**; **never purchasable** |
| Crystals | Premium currency | Priced/sized identically to Clash of Clans gems; buys dog slots, rate-limit relief, cosmetics |

Core loop: dogs at the park stack Energy; scheduled events offer wake-up
progress and require dogs physically present to win. Gameplay is occasional
management choices, not twitch input — this shapes the netcode requirements
below (no client prediction needed).

Social events broadcast live to all clients in the park with a tight SLA —
"Andy added BimBim to Charsiu's pack" must land while it is still relevant
to act on. Target: p99 ≤ 2s end-to-end for social/presence events; world
sim ticks at 24Hz.

## Architecture invariants

1. **Server-authoritative simulation.** The server's world state is the only
   truth. Clients never mutate world state; they send intents, the sim
   applies them. Anything purchasable or rankable (Energy, Coin, Favor, Fur,
   Crystals, prizes, check-ins) is computed server-side without exception.

2. **Clients render by snapshot interpolation; resync is the recovery path.**
   Terminology, precisely: today every 24Hz tick is a full authoritative
   snapshot and the client presentation module interpolates between the last
   two (with frame-jank clamping and snap-on-teleport). "Periodic resync"
   becomes the right description when ticks move to delta encoding
   (#1328): baseline keyframe + deltas + periodic keyframes, where a client
   that misses deltas snaps to the next keyframe. No client prediction:
   management gameplay does not need it, and omitting it keeps every surface
   trivially consistent.

3. **One deterministic core, unchanged, on every surface.** Game logic
   compiles from the shared Rust structural core (`//src/services/presenced/sim`)
   to wasm. The module bytes are the portability contract. Three rules keep
   the determinism absolute:
   - **Fixed-point only.** The sim is integer arithmetic throughout
     (fractional values in Q16.16); float types are banned from the wasm
     modules and enforced at build time twice — a source token gate
     (`sim:no_float_test`) and a wasm binary scan for float value-type
     declarations (`presenced_test`). No FPU, rounding mode, or NaN payload
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

4. **Seamless updates: the ladder.** Every layer updates live, in order of
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
   - server binary: image roll (sessions resume by token);
   - network/routing: no coordination — see invariant 5.

5. **Disconnection is routine; clients auto-reconnect.** Any change to the
   network or routing layer may sever connections without ceremony, because
   every client redials with backoff and resumes by token. Resume restores
   the full session — including room membership (#1327 is a bug against this
   invariant, not a design choice). Measured under a 500-session
   simultaneous reconnect storm: resume gap p50 < 50ms, p99 < 500ms.

6. **Feature flags are presentation and product gating — never sim inputs.**
   Client-side flagging uses the OpenFeature SDK against our own flag
   control-plane service; clients hold a streaming subscription (targeting
   key: trainer id) so flips propagate live without reload, and cache last
   values for offline/reconnect gaps. The hard rule: a flag must never
   influence a behavior module's step function or any server-side resource
   computation — sim changes go through the dark-launch/promotion ladder
   where divergence is measured, not through flags. Flags gate UI, features,
   rollout cohorts, and kill switches.

7. **The economy is a ledger.** Energy/Coin/Favor/Fur/Crystals are TigerBeetle
   accounts and transfers (the payments plane already runs TigerBeetle;
   game currencies get their own ledgers). Purchases normalize through one
   entitlements service regardless of source: Apple IAP receipts and Google
   Play billing (mandatory for Crystals in app-store builds — a
   merchant-of-record cannot take those flows), Steam wallet if Steam
   graduates, and a MoR for web checkout (which MoR is TBD; not necessarily
   Stripe — the existing Stripe integration is sandbox-only and
   production-shaped, so the entitlements interface must stay
   provider-neutral). Fur is never purchasable by design; enforce it in the
   ledger topology, not in UI.

## Artifact inventory

The honest current decomposition, including what is *not* yet separated.

### Client-delivered

| Artifact | Status | Notes |
|---|---|---|
| Web shell | live (single-file `mythra.html`) | Connection mgmt, DOM, input, wasm host. Grows into the product shell; stays the same artifact across desktop web, Android web, iOS web |
| Rust→wasm sim core | live (crate, not shipped directly) | The shared deterministic core; compiled into every module below |
| Rust→wasm **server behavior module** | live (`live.wasm` / `shadow.wasm`) | Authoritative step; wazero |
| Rust→wasm **client presentation module** | live (`client.wasm`) | Interpolation/smoothing; hot-swapped via pong hash |
| Rust→wasm **client rules module** | planned | Pack-synergy/bonus preview math for UI, compiled from the same core the server uses to compute the real thing — previews can never drift from truth |
| Asset bundles | live (ConfigMap lane) | Content-addressed skins/tiles; graduates to CDN/R2 when size demands |
| macOS App Store app | planned | Thin WebKit shell around the web artifacts |
| iOS/iPadOS App Store app | planned | Own QUIC stack (WebKit's is the blocker, see gaps), wasm interpreter + AOT lane, Game Center |
| Android app (Play) | planned | WebView or JNI runtime embedding, Play Games, Play billing |
| Steam build | tentative | Decide after the three stores ship |

### Server — Go

| Artifact | Status | Notes |
|---|---|---|
| `presenced` | live, **deliberately monolithic** | One binary currently carries four layers: QUIC/WebTransport termination + session/token handling; room hub + tick fanout; behavior engine (wazero slots); page/module/asset HTTP serving. It has *not* been split. Split boundary when scale demands: gateway (transport/sessions) vs world-sim (rooms/ticks/behaviors), so parks can shard across nodes. Do not split before sharding forces it |
| Control plane | planned | Authentication (Guardian customer identity realm; Game Center / Play Games bridging), entitlements (SpiceDB-backed, purchase-source-neutral), billing normalization (IAP / Play / MoR / Steam → ledger), friends/social graph, dog-park registry (geo metadata + manual petition queue), feature-flag service (OpenFeature control plane with streaming subscriptions) |
| Data plane | planned | Persistent game state, distinct from the in-memory world sim: economy ledgers (TigerBeetle), check-in service (geo attestation, 5-minute sessions, presence-bonus windows), pack membership + inventory + mood scheduler, event feed fanout (the p99 ≤ 2s SLA), month-cycle orchestrator (22/6 clock, prize distribution, Fur grants, reset) |
| `loadgen` | live | Protocol load driver; ships in the presenced image |

### Tooling / QA

| Artifact | Status | Notes |
|---|---|---|
| `//src/services/presenced/sim:refresh` + lockstep diff tests | live | Committed wasm bytes provably match Rust source |
| `world_hash` oracle | live | Core export, stamped on ticks 1/s, re-derived and verified by every client (world ✓ pill); the cross-surface determinism assertion |
| `//qa:surfaces` runner | planned | One command: build → local server → scripted taps on every surface → all `world_hash` values equal at a barrier tick. `--devices` adds simulators + the physical rack |
| Device rack | planned (~$2.4k) | Mac mini controller (iOS automation host + simulators + macOS surface), iPhone (have), base iPad, Pixel, mid-tier Samsung, low-end MediaTek (the floor gate), powered hub, scrcpy/QuickTime mirror wall |

## Gaps and sequencing

1. **iOS web is blocked by Apple, not by us**: iOS 26.4 Safari ships
   WebTransport, but dialing presenced trips a Network.framework
   recursive-lock crash (repro captured; applies to all iOS browsers and
   WKWebView — they share the stack). File the Feedback, track betas. The
   native app with its own QUIC stack is the hedge, not a WKWebView wrapper.
2. **#1327** — resume loses room membership. Violates invariant 5; also
   corrupts pack semantics (an involuntary disconnect must not count as
   "leaving the park"). Fix before any pack feature ships.
3. **#1328** — tick datagrams exceed the QUIC limit past ~40 dogs/room.
   Fix direction is the delta-tick + keyframe design in invariant 2, plus
   interest management. Prerequisite for busy parks.
4. **MoR selection** — TBD; keep the entitlements interface provider-neutral.
5. **Steam** — tentative; no engineering spend until it graduates.

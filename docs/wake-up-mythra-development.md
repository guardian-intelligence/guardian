# Wake Up Mythra — development plan of record

Status: plan of record (2026-08). The wasm behavior stack and update ladder
described here are live at wakeupmythra.com; the architecture invariants,
netcode, and persistence plane are specified in src/chunkies/README.md and landing
now; everything in [Gaps and sequencing](#gaps-and-sequencing) is not.

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

Every supported surface gets e2e automation that gates the full release of
the components that reach it. One command (the planned `//qa:surfaces`
runner — not yet built, see QA section) runs the same scripted session —
join → spectate → behavior hot-flip → reconnect/resume — against every
surface and asserts the shared oracle
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
management choices, not twitch input — this shapes the netcode: events at
human decision rate, not state at tick rate (src/chunkies/README.md).

Social events broadcast live to all clients in the park with a tight SLA —
"Andy added BimBim to Charsiu's pack" must land while it is still relevant
to act on. Target: p99 ≤ 2s end-to-end for social/presence events; world
sim ticks at 24Hz.

## Artifact inventory

### Client-delivered

| Artifact | Status | Notes |
|---|---|---|
| Web shell | live (single-file `mythra.html`) | Connection mgmt, DOM, input, wasm host. Grows into the product shell; stays the same artifact across desktop web, Android web, iOS web |
| Rust→wasm sim core | live (crate, not shipped directly) | The shared deterministic core; compiled into every module below |
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
| `chunkies-gateway` | live | OIDC admission, WebTransport sessions, public HTTP, and routing to parks |
| `chunkies-chunkie` | live | one park authority per pod: simulation, journal, snapshots, and fan-out |
| Control plane | planned | Authentication (Guardian customer identity realm; Game Center / Play Games bridging), entitlements (SpiceDB-backed, purchase-source-neutral), billing normalization (IAP / Play / MoR / Steam → ledger), friends/social graph, dog-park registry (geo metadata + manual petition queue), feature-flag service (OpenFeature control plane with streaming subscriptions) |
| Data plane | planned | Persistent game state, distinct from the in-memory world sim: economy ledgers (TigerBeetle), check-in service (geo attestation, 5-minute sessions, presence-bonus windows), pack membership + inventory + mood scheduler, event feed fanout (the p99 ≤ 2s SLA), month-cycle orchestrator (22/6 clock, prize distribution, Fur grants, reset) |

### Tooling / QA

| Artifact | Status | Notes |
|---|---|---|
| `//src/chunkies/sim:refresh` + lockstep diff tests | live | Committed wasm bytes provably match Rust source |
| `world_hash` oracle | live | Core export, stamped on ticks 1/s, re-derived and verified by every client (world ✓ pill); the cross-surface determinism assertion |
| `//qa:surfaces` runner | planned | One command: build → local server → scripted taps on every surface → all `world_hash` values equal at a barrier tick. `--devices` adds simulators + the physical rack |
| Device rack | planned (~$2.4k) | Mac mini controller (iOS automation host + simulators + macOS surface), iPhone (have), base iPad, Pixel, mid-tier Samsung, low-end MediaTek (the floor gate), powered hub, scrcpy/QuickTime mirror wall |

## Gaps and sequencing

1. **iOS web is blocked by Apple, not by us**: iOS 26.4 Safari ships
   WebTransport, but dialing `chunkies-gateway` trips a Network.framework
   recursive-lock crash (repro captured; applies to all iOS browsers and
   WKWebView — they share the stack). File the Feedback, track betas. The
   native app with its own QUIC stack is the hedge, not a WKWebView wrapper.
2. **Email verification** — the customer realm has no SMTP configured and
   `verifyEmail` is off; the spectator/player OIDC gate (src/chunkies/README.md)
   enforces `email_verified` only once the realm can issue it. Adding SMTP
   to the realm JSON is the unblocking work.
3. **MoR selection** — TBD; keep the entitlements interface provider-neutral.
4. **Steam** — tentative; no engineering spend until it graduates.

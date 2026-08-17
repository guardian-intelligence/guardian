# Wake Up Mythra Netcode

"Wake Up, Mythra!" uses a shared server-authoritative deterministic simulation with periodic reconciliation. Product/platform plan: docs/wake-up-mythra-development.md. The framework is, broadly, called "chunkies". chunkies today is not a good fit for FPS games that need anti-wallhack features.

At a high level the goal is to minimize player interruptions as much as possible (loading screens, restarts, resyncs, jitter, lag, entity pop in/out), prevent abuse, minimize operating costs, and maximize development velocity so that players feel like they inhabit an immersive, exciting, and fair world.

### Fullstack

* Support 1 million concurrent connected players per AZ, 2000 per shard.
* QUIC transport, UDP, binary-prefix enveloped messages.
* "Riot Direct"-like north/south
* Server authoritative, no peer-to-peer.
* 1-tick delay -- Clients aim to stay 1 tick behind the server. SLA: <1% of clients >=2 ticks behind server for greater than 15 seconds for 3 of last 5 intervals.
* Disaster Recovery, total failure - Game simulation supports complete freeze to account for need to sync game simulation to real world time when recovering from backup. Upon freeze, allow clients to game world in frozen state until countdown to unfreeze.
* Shared deterministic simulation for physics, periodic reconciliation upon divergence/corruption with shared randomness seed.
* "Hot Patch"ed servers + client background OTA updates -- server can update configuration, runtime feature flags.

### Server-Side / Data Plane

* Worker nodes live in cluster, Talos with SecureBoot.
* Control-plane read replica located in same AZ.
* Unencrypted replicated LINSTOR (never writes sensitive data to disk) on fast NVMe. 
* No cloudflare (impossible, CF doesn't support UDP)
* 120tick servers, variable at runtime to handle load spikes.
* Reconnect Herd -- Connection acks scheduled and queued.
* Serially linearalized event processing -- multiple events arriving at the same timestamp are tiebroken against shared random seed.
* 0 Downtime -- server components independently deploy such that downtime is impossible by construction. Upon redeploy of ingress layer, clients signaled to switch between wt0, wt1, wt2 etc and wait for connections to drain before initiating deployment. Rolling update each ingress and monitor client behavior.
* Servers never process sensitive data.
* Node Failover -- Nodes are fungible. Server-side simulation that over-exert CPU are replicated to a stronger nearby nnode, traffic switches over.
* Real-world clock sync -- The server is physically located in specific geographic location so game simulation can be provided real-world time and updated arbitrarily. For disaster recovery scenarios, assume game world freezes between last recorded tick and first unfrozen tick.
* Disaster Recovery, Near-Zero-RPO - Every tick records all inputs received to colocated hot-path durable ledger. Durable ledger growth bounded to ring buffer wall-clock length (WUM uses 30 seconds) and backed up. Player inputs -> Server, per tick, acks all inputs and writes to in-mem store. Every batch of inputs per tick is guaranteed to be processed and factored into the next tick.
* Durable game world state (terrain, inventory, player position) stored in Postgres.
* No feature flags, only shadow replicas and rerouting.

### Client-Side

* Browser uses WebTransport, Android/iOS/iPad use native APIs.
* Cross-platform determinism -- fixed-point math, cross-platform WASM.
* Divergence/Corruption Dection -- Client hashes state at particular tick & epoch and checks against server, reconnects upon mismatch.
* Hot swapping -- version skew protection. Client receives signal that updated game simuation is available, downloads asset. Client auto swaps to new client when server emits epoch update. Client only runs one simulation at a time.
* Dynamic Predictive -- specific actions get prediction (movement, NPC intents, "over-time" effects like healing) while others require server ack. Prediction is bounded to avoid motion sickness upon resync, falling back to lockstep for everything. If no server ack within an upper bound, initiate force client reconnect flow. Snapping/teleportation is a falback (recover from server snapshot outside 30s ring), default is to smoothly increase simulation speed.
* Client-side game simulation interpolates between ticks.
* Mobile-plan friendly - stream input from server only and replaying them on client. Aiming to keep data transfer <10MB/hour of game time at p99. Clients stream assets at resolutions that factor in connection speed and type (wifi vs cellular), user-configurable.
* Entitlements update live on client without reconnect when control plane acks and updates server.
* Intents are queued (inspired by WoW)
* Asset handling -- Assets marked as blocking, "Nice to have", or "lazy".
* Feature flag subscriptions update without reload with per-ff salt to prevent the same players from being first to get a rollout.
* 1452-byte UDP MTU (max for IPv6), min across supported devices.

### Control Plane

| Where | What | Owns |
|---|---|---|
| `sim/park` (wasm, no_std) | the game rules | all state, validation (`sim_apply` rejects without mutating), hashing, snapshots |
| `sim/nav` | deterministic A* | pathing, `path_cost`; movement is state (position, waypoint, target — all hashed), never a cache |
| `sim/clock` (wasm, no_std) | client tick discipline | Acquiring → Locked (±2% slew) → FastForward (big deficits) → SnapshotRequired (beyond the ring). No floats, no host clocks — the host feeds it times and executes its step-count directives |
| `sim/session` (wasm, no_std) | the replica session | the wire codec, seq-dense event ordering, snapshot ring + rollback, resync/strike policy, intent identity + resend, and the own-intent prediction overlay over two host-held park slots (journal replica / presented). Time and transport are inputs; the host executes its verbs |
| `sim/client` (wasm) | presentation + the session ABI | render smoothing; re-exports the clock and the session. Never feeds back into world state |
| `mythrad/gateway.go` | public transport | WebTransport, OIDC-ticket admission, intent→actor binding, and park routing |
| `mythrad/park.go` | the authority | the anchored tick schedule, event stamping, journal append, hash ring, snapshot cadence, module swaps, and fan-out |
| `mythrad/park_server.go` | park boundary | authenticated gateway sessions and one configured park authority |
| `mythrad/journal` | durability | Postgres `park_events` / `park_snapshots` / `park_terrain`; per-park seq is dense and single-writer; `journaltest.Run` is the conformance suite |
| `packages/chunkies` | the game-agnostic replica host | moves opaque bytes between wire, wasm, and screen: the session module, the replica slot, the transport, and the guarded extension/projection doors a game layer reaches its own exports through. Knows no game vocabulary; the name is a deliberate find-and-replaceable placeholder |
| `packages/wum-client` | the game layer | WUM over the host: intent verbs, the HUD/view/terrain decodes, the glide presenter, and the isometric renderer. If TypeScript (or Go) can read a game rule, the rule is in the wrong place |
| `apps/wake-up-mythra-web/src/game` | the surface | platform adapters (WebTransport, fetch, auth), the HUD/stats/debug DOM, and the telemetry mapping. No protocol |

Not yet built, planned for the control plane:

* Auth, payments, entitlements, feature flags, geolocation checks.
* Disaster Recovery -- control plane houses sensitive customer data on encrypted Talos nodes with offsite backups. Backup decryption key is held by single principal or small group. Upon total node outage, restore is resuming from last snapshot + replaying events from PG.
* Anti-Bot -- Player intents are asynchronously streamed and replicated to CH for ML analysis.
* Anti-Bot -- Clients that request a ticket to establish a QUIC session must pass JIT bot-detection. Note that we support anonymous spectating (read only, no intents processed) as a customer acquisition top-of-funnel effort.

### Development

* Single Rust codebase for game simulation & performance-sensitive components. Game simulation split into shared/server-specific/client-specific.
* Rust game simulation tested with Deterministic Simulation Testing in full-stack harness.
* Golang for transport, routing components.

### Operations

* Cluster access gated to specific pod tags for SEV incidents, development change monitoring, and customer support with different privilege levels within each category.
* Deterministic replay -- Debugging customer issues can be done deterministicaly by tracing the timestamp to a known server epoch which traces back to a specific commit.

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

**How does a stale-but-correct client recover?** The clock measures its
deficit against verdict ticks: small → slew ±2%, seconds → fast-forward
(budgeted extra steps per frame), 
One state machine, property-tested in `sim/clock`; the dev debug panel and
the netsim proxy (`src/services/mythrad/README.md`) exist to torture it.

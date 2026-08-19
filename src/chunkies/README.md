# Chunkies Netcode

"Wake Up, Mythra!" (and all our games) use a shared server-authoritative deterministic simulation with dynamic reconciliation. The framework is, broadly, called "chunkies". chunkies today is not a good fit for FPS games that need anti-wallhack features.

At a high level the goal is to minimize player interruptions as much as possible (loading screens, restarts, resyncs, jitter, lag, entity pop in/out), prevent abuse, minimize operating costs, and maximize development velocity so that players feel like they inhabit an immersive, exciting, and fair world.

### Glossary

1. **Client** — browser or native app. One WebTransport session for game
   traffic to `wt.<domain>` (QUIC/UDP, TLS terminated on our metal —
   Cloudflare cannot proxy WebTransport); site, assets, and control-plane
   HTTPS to `www.<domain>` (TCP, Cloudflare-fronted). One wt front door for
   every game.
2. **Edge mesh** — the gateway fleet behind `wt.<domain>` DNS. A client
   connects to any gateway (*edge role*) and keeps that session for its
   whole visit: session lifetime is deliberately decoupled from where the
   simulation runs, so chunk migrations and pod rolls are edge-internal
   re-routes the client never sees. Intents are routed — never broadcast —
   by `(game, chunk)` through the chunk directory to the *home role*: the
   gateway on the node hosting the chunk. Fan-out is a multicast tree,
   possible because a chunk's tick stream is identical bytes for every
   subscriber: the chunkie emits once, the home gateway forwards once per
   interested edge node, the edge gateway fans out to its clients.
   Personalized frames (Welcome, acks) ride the edge session; catch-up
   snapshots are out-of-band content-addressed fetches, never mesh traffic.
   *(today: one node — edge and home are the same gateway process)*
3. **Availability zone** — a failure and locality domain: one or more
   bare-metal nodes in one place. Chunkie placement across nodes and AZs is
   the availability decision. *(today: one AZ, one game node — ash-worker0)*
4. **Node** — bare metal. Hosts the node-local gateway (hostNetwork) and
   chunkie pods. Chunkie pods are node-pinned by their WAL (local NVMe);
   gateways are fungible, chunkie pods are not.
5. **Chunkies mesh** — per game: the set of chunkie pods simulating that
   game's one shared world, plus the interchange lanes between neighbors.
   Each chunkie knows its neighbors; cross-chunkie traffic is egress/ingress
   travel records with generation fencing — never live reads. The mesh is
   configuration (game manifest + placement), not a Kubernetes object: each
   chunkie is a declared workload in the game's environment overlay.
6. **Chunkie** — one pod owning a group of chunks. Scaling up a hot region
   or draining a node means moving chunks between chunkies — the recovery
   path run on purpose: checkpoint, restore on the destination, edge
   re-route, fence the old generation. *(today: one chunk per pod — a WUM
   park)*
7. **Chunk** — the unit the framework can name: one owner per entity per
   tick, a complete replayable journal, its own hash channel, and the
   granularity of client subscription — interest management *is* chunk
   subscription. Everything the wire, the WAL, or another pod sees is
   chunk-granular; everything inside a single tick step is the game's
   business. The chunk is also the scheduling quantum inside a pod: chunk
   steps share nothing within a tick, so a chunkie ticks its chunks in
   parallel and each chunk steps on one core — a game declares chunks
   fine enough that its worst-case chunk fits a core's tick budget.
   Shape varies by game: a persistent region with neighbors (WUM: a
   park), or an instance — a chunk with empty adjacency and a session
   lifetime (a dungeon, a shooter lobby). A seam between chunks behaves
   identically whether the neighbor lives in the same pod, another
   chunkie, or another node: one-tick-lagged journaled ingress either
   way. Placement is invisible to gameplay by construction.
8. **In-chunk world** — game territory. The framework contract is the
   `Simulation` trait and nothing else; how a game organizes entities
   inside its wasm (an ECS is a natural choice, not a requirement) is its
   own business. The framework never reads game state.

Declared vs derived: a game declares its **chunk adjacency** — the shape of
its world — at deploy time in its manifest. Chunk→chunkie assignment is
**placement**, an operational decision that can change at runtime without
touching the game. Chunkie-to-chunkie edges are derived from chunk adjacency
plus placement, never declared; if they were declared by the game,
re-sharding a region would be a game change, and it must not be.

Interest and relevance are quantized to the chunk, never the event. Every
subscriber of a chunk receives identical bytes — that is what makes the
multicast tree and the journal hash possible — so per-player filtering
inside a chunk is structurally off the table (the anti-wallhack caveat
above, seen from the other side). The client host runs the game's
interest policy and turns position into a subscription set; the server's
role is coarse authorization (may this actor subscribe to this chunk?)
and game-blind routing. The same subscription set keys asset residency:
the client loads and unloads a chunk's content-addressed bundle as
subscriptions open and close, so time-to-interactive is one module plus
the entry chunk, everything else lazy. Steady state needs no snapshots at
all — a subscribed replica replays events; snapshots are pulled
(out-of-band, per-chunk, content-addressed) only at subscription open,
resync after divergence, or reconnect beyond the ring. This composition —
a deterministic replica per subscribed chunk, feed-forward seams between
them — is the experimental bet: replication engines scope by relevance
but cannot multicast; lockstep engines multicast but simulate everything.
The green test is the falling-sand loadgen: per-viewer downlink and
client compute O(subscribed), not O(world).

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
* Hot swapping -- version skew protection. Client receives signal that updated game simuation is available, downloads asset. Client auto swaps to new client when server emits epoch update. Client only runs one simulation at a time. Open constraint: a native iOS client cannot download executable modules (App Store 2.5.2 / 3.3.2) — assets stream freely, but module rollouts there either ride store releases (ship the next module dormant, activate on epoch) or the sim runs in a WebKit-executed context. Unresolved.
* Dynamic Predictive (client-side prediction with server reconciliation) -- specific actions get prediction (movement, NPC intents, "over-time" effects like healing) while others require server ack. Prediction is bounded to avoid motion sickness upon resync, falling back to lockstep for everything. If no server ack within an upper bound, initiate force client reconnect flow. Snapping/teleportation is a falback (recover from server snapshot outside 30s ring), default is to smoothly increase simulation speed. Reconciliation is a rebase: reset the presented slot to the replica, re-apply own intents not yet stamped or rejected, re-step the horizon; rejected intents drop out of the replay (reject purity), mispredictions smooth rather than snap. The one-tick seam bounds a rebase's blast radius to chunks within horizon seam-hops — in practice, the chunk you act in.
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
| `games/wake-up-mythra/sim/park` (wasm, no_std) | the game rules | all state, validation (`sim_apply` rejects without mutating), hashing, snapshots |
| `games/wake-up-mythra/sim/nav` | deterministic A* | pathing, `path_cost`; movement is state (position, waypoint, target — all hashed), never a cache |
| `chunkies/sim/client/clock` (wasm, no_std) | client tick discipline | Acquiring → Locked (±2% slew) → FastForward (big deficits) → SnapshotRequired (beyond the ring). No floats, no host clocks — the host feeds it times and executes its step-count directives |
| `chunkies-codec` (the spec at `src/chunkies/codec/spec/`, with Rust/Go/TS implementations) | the wire protocol, v5 | the golden vectors and caps table are the protocol; the three implementations are held to them. One shared unit, the EventRecord (`intent \| elen \| kind \| actor \| payload`): the same bytes are the client's intent envelope, the per-tick batch element, and (planned) the write-ahead record element, so an accepted intent is never re-encoded between arrival, fan-out, and disk |
| `chunkies/sim/client/session` (wasm, no_std) | the replica session | seq-dense event ordering over tick batches, snapshot ring + rollback, resync/strike policy, intent identity + resend, and the own-intent prediction overlay over two host-held sim slots (journal replica / presented). Time and transport are inputs; the host executes its verbs |
| `chunkies/sim/shared/abi` | the game↔host contract | the `Simulation` trait and `export_simulation!`: a game crate carries no unsafe and no extern surface, cannot misdeclare the ABI, and opts out of the content-fetch dance entirely with a zero content cap. The toy reference game (`chunkies/sim/shared/toy`) is the macro's proof and the conformance vehicle |
| `chunkies/sim/client` (wasm) | presentation + the session ABI | render smoothing; re-exports the clock and the session. Never feeds back into world state |
| `chunkies/gateway` | public transport | WebTransport, OIDC-ticket admission, uplink shaping, actor binding at ingress, and chunk routing |
| `chunkies/chunkie` | the runtime | the anchored tick schedule, event stamping, journal append, hash ring, snapshot cadence, module swaps, fan-out, and the trunk-facing chunk boundary |
| `chunkies/trunk` | internal transport | the authenticated, HMAC-fenced gateway↔chunkie multiplexing: one conn per pair, many attachments |
| `chunkies/mount` | module delivery | the behavior mount: hot-reloaded client/sim wasm slots and the committed defaults |
| `games/wake-up-mythra/services/wum` | the game's server vocabulary | kind numbers, dog-id binding, and the genesis terrain, for the game's own code and tests. The running host learns the same facts from the mounted game manifest (`game.conf`) — the framework imports nothing WUM-shaped |
| `chunkies/journal` | durability | Postgres `park_events` / `park_snapshots` / `park_terrain` (table names die with the PG journal); per-chunk seq is dense and single-writer; `journaltest.Run` is the conformance suite |
| `chunkies/gametest` | the game contract | the game-blind conformance suite over built artifacts: determinism, snapshot completeness, reject purity, system-event semantics; `wum` wires the committed modules through it |
| `src/chunkies/host/ts` | the game-agnostic replica host | moves opaque bytes between wire, wasm, and screen: the session module, the replica slot, the transport, and the guarded extension/projection doors a game layer reaches its own exports through. Knows no game vocabulary; the name is a deliberate find-and-replaceable placeholder |
| `src/games/wake-up-mythra/client` | the game layer | WUM over the host: intent verbs, the HUD/view/terrain decodes, the glide presenter, and the isometric renderer. If TypeScript (or Go) can read a game rule, the rule is in the wrong place |
| `src/games/wake-up-mythra/web/src/game` | the surface | platform adapters (WebTransport, fetch, auth), the HUD/stats/debug DOM, and the telemetry mapping. No protocol |

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
chunk reopens on exactly the tick the wall clock defines. Measured: a chunk
minutes deep reopens in under a second; drilled live three times with a
player connected.

**What if the wall clock itself jumps?** Forward: the schedule demands
catch-up ticks, bounded per wakeup; past the hash ring the chunk closes and
repays the gap through the dark reopen path. Backward (NTP step, lying
RTC): the chunk never unticks and never idles backward — it simply waits for
reality to catch up, visible as negative `chunkies_tick_lag_seconds`.

**A client sends an intent during a server blip — then what?** Today: lost.
Intents are idempotent by `(actor, intent_id)`, so client-side
resend-after-rejoin is safe by design but not yet implemented. Backlog.

**Storage math (exercise).** A journal row is ~120B (fields + payload +
tuple overhead); a snapshot every 512 events. Small chunk, 20 players × 2
acts/min: 57,600 events/day ≈ 7MB + ~112 snapshots ≈ 15MB/day — years per
10GB. The 2000-bot drill measured 1–2GB/day: scale is linear in *human*
action rate, and ticks cost zero. Rerun the math before believing any
retention urgency.

**What does an idle session cost?** ~100KB/hour downlink (checks +
verdicts). An *active* chunk scales with total human activity × occupancy —
the 2000-active drill measured ~20MB/hour/session. A mega-chunk needs
interest management (culling the fan-out) before it meets the idle target;
tracked as future work.

**How does a stale-but-correct client recover?** The clock measures its
deficit against verdict ticks: small → slew ±2%, seconds → fast-forward
(budgeted extra steps per frame),
One state machine, property-tested in `sim/clock`; the dev debug panel and
the netsim proxy (`src/games/wake-up-mythra/README.md`) exist to torture it.

**How expensive can strangers make my join?** The one place a client
re-executes history is subscription catch-up: snapshot fetch plus journal
tail replay. Per-tick replica cost is input-volume-independent (others'
events are stamped once into the replica; a rebase replays only your own
intents), so the adversarial knob is event *density*: worst-case tail ≈
snapshot interval × chunk population × per-actor rate cap. That product
is a chunk-sizing floor — a chunk whose worst-case tail can't replay
inside the join-time budget is too big or too permissive.

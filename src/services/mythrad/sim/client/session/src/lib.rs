//! The replica session core: everything between the wire and the park
//! module that decides *what the world is*. Frame codec, seq-dense event
//! ordering, temporal rollback, resync and strike policy, check cadence,
//! intent identity, reject policy, and the module-swap choreography all
//! live here, so every host — browser, app, load bot — inherits identical
//! netcode behavior instead of reimplementing it.
//!
//! Pure by construction: time is always a parameter, entropy arrives as
//! the `init` nonce, and every effect leaves through the `Host` trait
//! (park verbs, transport writes, inflate, host requests, telemetry). No
//! allocator, no floats, no clocks.
//!
//! One park: the journal replica. It carries journal events only, in
//! dense seq order at their tick, and the renderer and HUD read it
//! directly. A snapshot ring (depth 10, one per second) plus recent-event
//! replay backs rollback; the hash ring and the checks read the same
//! world. An intent is sent and tracked until the journal answers it, and
//! is never applied here — the client shows what the authority has
//! journaled, and the wait for it is the round trip and nothing more.
//! Corrections that do arrive — a restore, a repair — are announced with
//! S_CORRECTED so the host can absorb them at view level — every
//! mutation of the world that is not the step function is announced.
//!
//! WHAT A CLIENT MAY SHOW AHEAD OF THE JOURNAL. The test is whether an
//! intent's effect can reach the step function. An intent that only
//! writes fields nothing steps on could be shown early and stay true. One
//! whose effect the step function reads ACCUMULATES — the world keeps
//! deriving from it every tick — and showing that early means simulating
//! it, which means a second world, which cannot be kept honest: the
//! accumulation is a function of when the action started, and the
//! authority starts it later than the client does.
//!
//!   move_to    sets a target, the dog walks, position accumulates.
//!   boost_set  writes a flag. The flag doubles speed, so position
//!              accumulates: it reads as a pure edit and is not one.
//!   check_in   energy and a day stamp, which nothing steps on.
//!   join       a spawn cell drawn from the tick it is applied at, and a
//!              dependency everything else waits on.
//!
//! Three separate things have made a kind unshowable, and they are worth
//! keeping distinct because a new kind can fail any one alone: an outcome
//! drawn from the tick it is applied at, a dependency that has to exist
//! first, and an effect the step function reads.
//!
//! The ruling, 2026-08-14, and the reason this file simulates one world:
//! the client predicts nothing. "Instant" means no overhead over the
//! round trip, not less than it — latency is answered by putting the
//! authority near the player, not by a second simulation. Tap feedback
//! that is presentation (a marker, a turn, dust) is instant and lives in
//! the host; motion waits for the journal. Do not reintroduce prediction
//! without a new product requirement.
//!
//! Wire protocol v4 (little-endian scalars; the QUIC varint length prefix
//! is big-endian per RFC 9000 §16) is in [`wire`].
//!
//! A verdict is evidence about the history the replica held when it asked,
//! and both of its negative answers mean two different things. "Unknown" is
//! either a tick that fell out of the authority's ring or one it has not
//! stamped yet, and only the first is staleness — the second is this replica
//! racing the authority's present, which it does by design after a restore
//! and would do more often at a shorter lag. "Mismatch" is either history
//! that was wrong or history this replica has since rewritten underneath the
//! check: a late event repairs the very tick a check named, so the hash the
//! authority disagrees with is one that no longer exists here. Both are
//! classified at the strike, because either one costs a resync — a
//! correction the viewer sees — for a replica that is already right. The
//! second costs a check its tick, kept until the verdict lands and marked by
//! any repair that rewrites it.
#![cfg_attr(not(test), no_std)]

use core::mem;
use mythra_sim_clock::{Clock, F_SNAPSHOT, STEP_COST_US, State};

pub mod wire;

/// Host requests: the core names what it needs, the host fetches it.
pub const REQ_NEED_TERRAIN: u32 = 1;
pub const REQ_NEED_MODULE: u32 = 2;
pub const REQ_RESYNC_WANTED: u32 = 3;
/// The stream can no longer be read: the host must close the transport and
/// redial. A resync cannot repair lost framing — it travels on the same
/// broken stream, and QUIC will happily keep that stream alive forever.
pub const REQ_TEARDOWN: u32 = 4;

/// Telemetry vocabulary — numeric only, mapped to spans by the host.
pub const T_CONNECTED: u32 = 1;
pub const T_WELCOME: u32 = 2;
pub const T_EVENT_APPLIED: u32 = 3;
/// rollback(returned_to, late << 32 | rewound). The first is the absolute
/// tick the repair returned to, so the range it rewrote — `[returned_to -
/// rewound, returned_to]` — and the event that caused it — `returned_to -
/// late` — are facts in the trace rather than arithmetic over frame
/// timestamps. The two distances ride one slot because they answer
/// different questions and reading one for the other inverts the meaning:
/// LATE is how far behind the replica an event arrived, which is the
/// defect signal, and REWOUND is how far back the ring's cadence forced
/// the reach, which is repair cost and gates nothing.
pub const T_ROLLBACK: u32 = 4;
pub const T_RESYNC_REQUESTED: u32 = 5;
pub const T_SNAPSHOT_RESTORED: u32 = 6;
pub const T_CHECK_SENT: u32 = 7;
pub const T_VERDICT: u32 = 8;
pub const T_MISMATCH: u32 = 9;
pub const T_REJECT: u32 = 10;
pub const T_MODULE_SWAP_WANTED: u32 = 11;
pub const T_MODULE_SWAPPED: u32 = 12;
pub const T_CLOCK_STATE: u32 = 13;
pub const T_INTENT_SENT: u32 = 14;
pub const T_AUTO_REJOIN: u32 = 15;
pub const T_RESTORE_FAILED: u32 = 16;
/// presence(source, tick): 1 the journal placed our dog, 2 the authority
/// refused a join as already present. Which one a session gets decides
/// when its held intents go out.
pub const T_PRESENCE: u32 = 17;
pub const PRESENCE_JOURNAL: u64 = 1;
pub const PRESENCE_ALREADY_THERE: u64 = 2;
/// replayed(seq, tick): an event a repair re-applied while rebuilding
/// history, emitted before the attempt so a refused one is visible too.
/// These fall between the `T_ROLLBACK` span that opened the repair and
/// whatever ends it, which is what lets a reader pair a repair with the
/// events it actually touched — the one thing a trace could not show, and
/// the reason a replay defect can only be narrowed from outside rather
/// than confirmed.
pub const T_REPLAYED: u32 = 18;
/// arrived(event tick, replica tick): where the replica stood when an
/// event reached it, before anything is done with it. The difference is
/// the margin the event arrived with — positive means it beat the replica
/// to its own tick, negative means it was already late and a repair is
/// owed. Paired with the replica's measured trail, this is the authority's
/// stamp-to-wire delay: the lead is sized against the network, and nothing
/// measures the pipeline the events actually travel through, so without
/// this the delay can only be inferred from how late repairs are.
pub const T_EVENT_ARRIVED: u32 = 19;
/// answered((resends << 16) | kind, latency_ms): the journal applied an
/// event carrying one of OUR pending intent ids — the moment the
/// player's action became world state. The latency is measured HERE,
/// from the intent's first wire write to this apply, so every host
/// reports the same figure with zero bookkeeping: the number game feel
/// is judged by, as a finished fact. The kind rides as its wire number,
/// so a new action reaches dashboards without a telemetry change — only
/// the host's name table grows.
pub const T_INTENT_ANSWERED: u32 = 20;
/// resent(kind, resends): a pending intent went back on the wire under
/// its original id (the authority dedupes by id, so this can never
/// double-apply) — a reconnect or presence-confirmation flush. The
/// answered fact carries the final count; this marks the retry itself.
pub const T_INTENT_RESENT: u32 = 21;
/// dropped((reason << 16) | kind, held_ms): a pending intent was
/// discarded and will never be answered — the player acted and the world
/// will not reflect it. Reasons below; held_ms is how long it waited
/// (0 for one that never reached the wire). Without this the loss is
/// invisible: no reject arrives, no event applies, the action just
/// vanishes.
pub const T_INTENT_DROPPED: u32 = 22;
/// A 33rd in-flight intent evicted the oldest.
pub const DROP_OVERFLOW: u64 = 1;
/// `reidentify`: the old identity's intents die with it.
pub const DROP_REIDENTIFY: u64 = 2;

/// The diagnostics record (`Session::diag`) layout version and size.
/// Little-endian throughout:
///   0  u16 version
///   2  u8  clock state (0 acquiring, 1 locked, 2 fast-forward,
///          3 snapshot-required)
///   3  u8  reserved
///   4  u32 rtt_ms
///   8  i64 trail_q16   (behind the authority's present)
///   16 i64 error_q16   (behind the cushioned target)
///   24 u64 tick
///   32 i64 seq
///   40 u32 trail target, ticks (the invariant, stated by the module)
///   44 u32 operating cushion, ticks
///   48 u64 ×6 counters: events, rollbacks, resyncs, checks, mismatches,
///          rejects
pub const DIAG_VERSION: u16 = 1;
pub const DIAG_BYTES: usize = 96;

/// Why the replica asked for a snapshot. Rides both `REQ_RESYNC_WANTED`
/// and `T_RESYNC_REQUESTED`.
pub const R_CLOCK: u32 = 1;
pub const R_LATE_EVENT: u32 = 2;
pub const R_EVENT_REJECTED: u32 = 3;
pub const R_HASH_MISMATCH: u32 = 4;
pub const R_CHECK_AGED_OUT: u32 = 5;
pub const R_MODULE_EPOCH: u32 = 6;
pub const R_TERRAIN_FETCH: u32 = 7;
pub const R_RESTORE_FAILED: u32 = 8;
pub const R_QUEUE_OVERFLOW: u32 = 9;
pub const R_MODULE_SWAPPED: u32 = 10;
pub const R_STREAM_OVERFLOW: u32 = 11;
pub const R_FRAMING: u32 = 12;
/// A replayed event the park refused. The replica cannot rebuild the
/// history it is holding, and nothing will re-deliver it: only a snapshot
/// repairs this, and carrying on would leave the world quietly wrong.
pub const R_REPLAY_REFUSED: u32 = 13;

/// `session_pump` status bits.
pub const S_HAVE_STATE: u32 = 1;
pub const S_RESYNCING: u32 = 1 << 1;
pub const S_STEPPED: u32 = 1 << 2;
pub const S_WANT_TERRAIN: u32 = 1 << 3;
pub const S_WANT_MODULE: u32 = 1 << 4;
/// The world was corrected during this pump, so dogs may have moved
/// without time passing. The host reads this on the same frame and
/// carries the difference as a decaying per-dog offset; a frame later is
/// a frame too late, because by then it has already been drawn.
///
/// Four paths correct the world. Each one raises this bit, and a fifth
/// would have to:
///
///   1. A snapshot restore, in `land` — wherever it lands, including
///      between two pumps, which is why the flag is latched here rather
///      than derived at the end of one.
///   2. A repair, in `rollback_to`: both the rewind and its replay.
///   3. A `terrain_set` event, which puts every dog the new blob cannot
///      stand on back onto ground that exists. It is the only apply that
///      moves a dog already in the park.
///   4. A module swap, which drops the world outright. The replica holds
///      no state until a snapshot lands, so the swap is announced by the
///      restore in (1) that completes it.
///
/// The step function is the only other way the world changes and the only
/// one that is not a correction: it is time passing, and it reports
/// itself as S_STEPPED. `clock_skip` moves the park's tick without a step,
/// which is why the tick is read back from the park after every apply
/// instead of assumed.
///
/// The enumeration is mechanical rather than remembered: `sim_step` is the
/// only thing in the park that moves a dog, `sim_restore` and `sim_apply`
/// commit nothing on the paths that fail, and the park's own test pins
/// that no event kind but `terrain_set` (and a `join`, which has no prior
/// position to keep) relocates one.
pub const S_CORRECTED: u32 = 1 << 5;
/// Clock state (0 acquiring, 1 locked, 2 fast-forward, 3 snapshot) at bit 8.
pub const S_CLOCK_SHIFT: u32 = 8;

pub const ROLE_SPECTATOR: u32 = 0;
pub const ROLE_PLAYER: u32 = 1;

/// Stat selectors for `session_stat`.
pub const STAT_EVENTS: u32 = 1;
pub const STAT_ROLLBACKS: u32 = 2;
pub const STAT_RESYNCS: u32 = 3;
pub const STAT_CHECKS: u32 = 4;
pub const STAT_MISMATCHES: u32 = 5;
pub const STAT_REJECTS: u32 = 6;

// The park ABI values this core has to reason about. The park crate is the
// definition; duplicating the handful used here keeps the game rules out
// of the client tier and off the client.wasm export table.
const EV_JOIN: u16 = 1;
const EV_CHECK_IN: u16 = 3;
const EV_MOVE_TO: u16 = 4;
const EV_EPOCH_ADVANCE: u16 = 6;
/// The one journal kind whose application moves dogs: every dog the new
/// blob cannot stand on is put back on the nearest ground it can.
const EV_TERRAIN_SET: u16 = 7;
const EV_BOOST_SET: u16 = 8;
const ERR_PRESENT: u32 = 2;
const ERR_ABSENT: u32 = 3;
const ERR_NOOP: u32 = 10;
/// `sim_restore` refusing a snapshot taken on other terrain.
const RESTORE_WRONG_TERRAIN: u32 = 4;

/// One snapshot per second of rollback depth.
const RING_DEPTH: usize = 10;
/// Floors recorded while walking back to the present after a rollback.
/// Steady state costs nothing — these are written only during a repair —
/// but without them each repair leaves the newest floor aging where it
/// was, so the next one rewinds further than the last: measured climbing
/// 4, 5, 7, 9 ... 23 ticks for events that were each one tick late.
const REPAIR_DEPTH: usize = 16;
/// A full park's canonical snapshot plus headroom (the park's io buffer).
const SNAP_CAP: usize = 64 * 1024;
/// Frame reassembly. The buffer must hold a whole frame that is still
/// being delivered PLUS a full host chunk landing on top of it, or a
/// chunk boundary inside the largest frame would overflow and lose byte
/// alignment: 64 KiB of host staging + a snapshot frame carrying the
/// park's 64 KiB io buffer and its 41-byte framing, rounded up.
const IN_CAP: usize = 160 * 1024;
const OUT_CAP: usize = 4 * 1024;
const HASH_RING: usize = 1024;
const MAX_QUEUED: usize = 256;
const MAX_RECENT: usize = 512;
const MAX_INTENTS: usize = 32;
/// Checks whose verdict has not come back yet. The cadence is seconds and
/// a verdict rides one round trip, so this is depth for a pathological
/// cadence rather than a normal one.
const MAX_CHECKS: usize = 8;
const EV_PAYLOAD_CAP: usize = 64;
const HELLO_MS: u64 = 1500;
const RETRY_MS: u64 = 3000;
const AUTO_JOIN_MS: u64 = 5000;
/// How long an unanswered resync waits before being asked again. The same
/// two seconds the clock re-requests on, for the same reason: the first
/// ask can race a dying transport.
const RESYNC_REASK_MS: u64 = 2000;
const STRIKES_TO_RESYNC: u32 = 2;

#[derive(Clone, Copy)]
struct Ev {
    seq: i64,
    tick: u64,
    kind: u16,
    intent: u64,
    plen: u16,
    p: [u8; EV_PAYLOAD_CAP],
}

impl Ev {
    const EMPTY: Ev = Ev {
        seq: 0,
        tick: 0,
        kind: 0,
        intent: 0,
        plen: 0,
        p: [0; EV_PAYLOAD_CAP],
    };

    fn payload(&self) -> &[u8] {
        &self.p[..self.plen as usize]
    }
}

#[derive(Clone, Copy)]
struct Intent {
    /// The only handle an event carries back to its sender — the wire
    /// dropped the actor, so an event is ours exactly when this full u64
    /// matches a pending entry. Never infer ownership any other way, and
    /// never match on the counter half: `(nonce << 32) | counter` is what
    /// keeps two senders (and two page loads of one sender) from claiming
    /// each other's events.
    id: u64,
    kind: u16,
    plen: u16,
    /// The connection this intent last went out on, or 0 for one never
    /// sent — minted before the dog's presence was confirmed, or left over
    /// from a connection that died first.
    sent_conn: u32,
    /// Wall clock at the FIRST wire write (0 while held for presence).
    /// The per-action latency is measured from here, in the core, so
    /// every host inherits the same figure instead of pairing spans.
    sent_ms: u64,
    /// Wire writes past the first, under the same id.
    resends: u16,
    p: [u8; EV_PAYLOAD_CAP],
}

impl Intent {
    const EMPTY: Intent = Intent {
        id: 0,
        kind: 0,
        plen: 0,
        sent_conn: 0,
        sent_ms: 0,
        resends: 0,
        p: [0; EV_PAYLOAD_CAP],
    };

    fn payload(&self) -> &[u8] {
        &self.p[..self.plen as usize]
    }
}

#[derive(Clone, Copy)]
struct Snap {
    tick: u64,
    seq: i64,
    len: usize,
    state: [u8; SNAP_CAP],
}

impl Snap {
    const EMPTY: Snap = Snap {
        tick: 0,
        seq: 0,
        len: 0,
        state: [0; SNAP_CAP],
    };
}

#[derive(Clone, Copy)]
struct HashEntry {
    tick: u64,
    hash: u64,
    valid: bool,
}

impl HashEntry {
    const EMPTY: HashEntry = HashEntry {
        tick: 0,
        hash: 0,
        valid: false,
    };
}

/// A check on the wire, and whether the history it described is still the
/// one this replica holds.
#[derive(Clone, Copy)]
struct Check {
    tick: u64,
    replaced: bool,
}

impl Check {
    const EMPTY: Check = Check {
        tick: 0,
        replaced: false,
    };
}

/// Everything the core asks of its embedder. There is one park — the
/// journal replica — and the host copies bytes between its memory and
/// this one on the park verbs.
pub trait Host {
    fn park_apply(&mut self, ev: &[u8]) -> u32;
    fn park_step(&mut self);
    fn park_snapshot(&mut self, dst: &mut [u8]) -> u32;
    fn park_restore(&mut self, state: &[u8]) -> u32;
    fn park_hash(&mut self) -> u64;
    fn park_tick(&mut self) -> u64;
    fn send_stream(&mut self, frame: &[u8]);
    fn send_datagram(&mut self, datagram: &[u8]);
    /// Raw deflate; returns the inflated length, 0 on error.
    fn inflate(&mut self, src: &[u8], dst: &mut [u8]) -> u32;
    fn request(&mut self, kind: u32, a: u64);
    fn emit(&mut self, kind: u32, a: u64, b: u64);
}

pub struct Session {
    clock: Clock,
    my_dog: u64,
    role: u32,
    check_ms: u64,
    nonce: u32,
    counter: u32,

    seq: i64,
    tick: u64,
    hz: u64,
    terrain_id: u64,

    connected: bool,
    /// Bumped per dial, so an intent can tell "already sent on this
    /// connection" from "sent on the one that just died".
    conn: u32,
    /// The authority has confirmed our dog is in the park — by journaling
    /// our join, or by refusing it as already present. Intents that need a
    /// dog wait for this rather than racing it.
    presence: bool,
    have_state: bool,
    visible: bool,
    /// The world was corrected and no pump has reported it yet. Not every
    /// correction happens inside a pump — a snapshot arriving mid-frame
    /// restores where it lands — and the host has to hear about all of
    /// them, so this is latched here and cleared when a pump carries it.
    corrected: bool,
    replaying: bool,
    resyncing: bool,
    /// When the outstanding resync went out. A resync asked for on the
    /// event path has nothing else watching it: the clock re-asks when it
    /// is starved, but a repair that refused and asked for a snapshot
    /// instead would wait forever, and the session goes quiet rather than
    /// degraded — no event applies again, because the seq gap never closes.
    resync_at_ms: u64,
    /// Why the outstanding resync was asked for. A retry repeats it rather
    /// than naming itself: it is the same request, and a reason that
    /// changed on retry would show a dashboard two causes where there was
    /// one.
    resync_reason: u32,
    resync_on_landed: bool,
    module_latch: bool,
    wrong_terrain: bool,
    park_pw: u32,
    strikes: u32,

    held_valid: bool,
    held_seq: i64,
    held_tick: u64,
    held_wh: u64,
    held_terrain: u64,
    held_len: usize,
    land_pending: bool,
    retry_pending: bool,
    retry_at_ms: u64,
    deferred_resync: u32,

    /// The frame budget the host last pumped with, so a snapshot landing
    /// between pumps can price its catch-up the same way.
    last_budget_us: u32,
    next_check_ms: u64,
    checks: [Check; MAX_CHECKS],
    checks_len: usize,
    auto_joined: bool,
    last_auto_join_ms: u64,
    clock_state: u32,
    stats: [u64; 8],

    in_len: usize,
    ring_head: usize,
    ring_len: usize,
    repair_head: usize,
    repair_len: usize,
    repairing: bool,
    queued_len: usize,
    recent_head: usize,
    recent_len: usize,
    intents_len: usize,
}

/// The core's storage, in its own static so it stays zero-initialized:
/// folded into `Session` it would ride the module's data section as a
/// megabyte of literal zeroes, and client.wasm ships in a ConfigMap.
struct Bufs {
    inbuf: [u8; IN_CAP],
    out: [u8; OUT_CAP],
    state: [u8; SNAP_CAP],
    hold: [u8; SNAP_CAP],
    ring: [Snap; RING_DEPTH],
    repair: [Snap; REPAIR_DEPTH],
    hashes: [HashEntry; HASH_RING],
    queued: [Ev; MAX_QUEUED],
    recent: [Ev; MAX_RECENT],
    intents: [Intent; MAX_INTENTS],
}

static mut BUFS: Bufs = Bufs {
    inbuf: [0; IN_CAP],
    out: [0; OUT_CAP],
    state: [0; SNAP_CAP],
    hold: [0; SNAP_CAP],
    ring: [Snap::EMPTY; RING_DEPTH],
    repair: [Snap::EMPTY; REPAIR_DEPTH],
    hashes: [HashEntry::EMPTY; HASH_RING],
    queued: [Ev::EMPTY; MAX_QUEUED],
    recent: [Ev::EMPTY; MAX_RECENT],
    intents: [Intent::EMPTY; MAX_INTENTS],
};

fn bufs() -> &'static mut Bufs {
    unsafe { &mut *(&raw mut BUFS) }
}

impl Session {
    pub const NEW: Session = Session {
        clock: Clock::NEW,
        my_dog: 0,
        role: ROLE_SPECTATOR,
        check_ms: 5000,
        nonce: 0,
        counter: 0,

        seq: 0,
        tick: 0,
        hz: mythra_sim_clock::GENESIS_HZ,
        terrain_id: 0,

        connected: false,
        conn: 0,
        presence: false,
        have_state: false,
        visible: true,
        corrected: false,
        replaying: false,
        resyncing: false,
        resync_at_ms: 0,
        resync_reason: 0,
        resync_on_landed: false,
        module_latch: false,
        wrong_terrain: false,
        park_pw: 0,
        strikes: 0,

        held_valid: false,
        held_seq: 0,
        held_tick: 0,
        held_wh: 0,
        held_terrain: 0,
        held_len: 0,
        land_pending: false,
        retry_pending: false,
        retry_at_ms: 0,
        deferred_resync: 0,

        last_budget_us: 8_000,
        next_check_ms: 0,
        checks: [Check::EMPTY; MAX_CHECKS],
        checks_len: 0,
        auto_joined: false,
        last_auto_join_ms: 0,
        clock_state: 0,
        stats: [0; 8],

        in_len: 0,
        ring_head: 0,
        ring_len: 0,
        repair_head: 0,
        repair_len: 0,
        repairing: false,
        queued_len: 0,
        recent_head: 0,
        recent_len: 0,
        intents_len: 0,
    };

    /// One page load's identity and cadence. `nonce` is the high half of
    /// every intent id this load mints: uniqueness across page loads for
    /// one subject is required, because the server's idempotency window
    /// spans reconnects and a counter restarting at 1 would collide with
    /// the previous load's ids.
    pub fn init(&mut self, my_dog: u64, role: u32, check_ms: u32, nonce: u32, now_ms: u64) {
        self.reset();
        self.my_dog = my_dog;
        self.role = role;
        self.check_ms = check_ms.max(100) as u64;
        self.nonce = nonce;
        self.next_check_ms = now_ms + HELLO_MS;
    }

    fn reset(&mut self) {
        self.clock = Clock::NEW;
        self.my_dog = 0;
        self.role = ROLE_SPECTATOR;
        self.check_ms = 5000;
        self.nonce = 0;
        self.counter = 0;
        self.seq = 0;
        self.tick = 0;
        self.hz = mythra_sim_clock::GENESIS_HZ;
        self.terrain_id = 0;
        self.connected = false;
        self.conn = 0;
        self.presence = false;
        self.have_state = false;
        self.visible = true;
        self.corrected = false;
        self.replaying = false;
        self.resyncing = false;
        self.resync_at_ms = 0;
        self.resync_reason = 0;
        self.resync_on_landed = false;
        self.module_latch = false;
        self.wrong_terrain = false;
        self.park_pw = 0;
        self.strikes = 0;
        self.held_valid = false;
        self.land_pending = false;
        self.retry_pending = false;
        self.retry_at_ms = 0;
        self.deferred_resync = 0;
        self.last_budget_us = 8_000;
        self.next_check_ms = 0;
        self.checks_len = 0;
        self.auto_joined = false;
        self.last_auto_join_ms = 0;
        self.clock_state = 0;
        self.stats = [0; 8];
        self.in_len = 0;
        self.ring_head = 0;
        self.ring_len = 0;
        self.repair_head = 0;
        self.repair_len = 0;
        self.repairing = false;
        self.queued_len = 0;
        self.recent_head = 0;
        self.recent_len = 0;
        self.intents_len = 0;
        self.hash_clear();
    }

    /// A sign-in upgrade mid-session: a new dog, a new role, a new intent
    /// id space. The replica's bookkeeping survives — the host redials
    /// right after this, and that hello carries the seq we already hold,
    /// so the upgrade costs a reconnect rather than a fresh world. Pending
    /// intents do not survive: they were the old dog's.
    pub fn reidentify<H: Host>(
        &mut self,
        h: &mut H,
        my_dog: u64,
        role: u32,
        nonce: u32,
        now_ms: u64,
    ) {
        for i in 0..self.intents_len {
            let it = bufs().intents[i];
            h.emit(
                T_INTENT_DROPPED,
                (DROP_REIDENTIFY << 16) | it.kind as u64,
                held_ms(&it, now_ms),
            );
        }
        self.my_dog = my_dog;
        self.role = role;
        self.nonce = nonce;
        self.counter = 0;
        self.intents_len = 0;
        self.presence = false;
        self.auto_joined = false;
        self.last_auto_join_ms = 0;
    }

    pub fn set_visible(&mut self, visible: bool) {
        self.visible = visible;
    }

    pub fn tick(&self) -> u64 {
        self.tick
    }

    pub fn seq(&self) -> i64 {
        self.seq
    }

    /// Render interpolation alpha: the frame's phase within the current
    /// tick, Q16 in [0, ONE).
    pub fn phase_q16(&self) -> u32 {
        self.clock.phase_q16()
    }

    pub fn rtt_ms(&self) -> u32 {
        (self.clock.rtt_ms_q16() >> 16) as u32
    }

    /// Phase error in Q16 ticks, positive when the replica trails its
    /// target. Diagnostics only.
    pub fn error_q16(&self, now_ms: u64) -> i64 {
        self.clock.error_q16(now_ms, self.tick)
    }

    /// How far the replica trails the authority's present, Q16 ticks —
    /// the figure `clock::TRAIL_TARGET_TICKS` bounds. Diagnostics only.
    pub fn trail_q16(&self, now_ms: u64) -> i64 {
        self.clock.trail_q16(now_ms, self.tick)
    }

    pub fn stat(&self, kind: u32) -> u64 {
        match kind {
            1..=6 => self.stats[kind as usize - 1],
            _ => 0,
        }
    }

    /// The diagnostics record: one raw dump of the session's live state,
    /// versioned and little-endian, for a host to parse, forward, and
    /// discard. This is the ONLY state read a pane or a harness needs —
    /// a new diagnostic is a new field here and a line in the host's
    /// decoder, never a new ABI export. The record states its own trail
    /// target so no host carries a mirrored constant to drift.
    pub fn diag(&self, now_ms: u64, dst: &mut [u8]) -> usize {
        if dst.len() < DIAG_BYTES {
            return 0;
        }
        dst[0..2].copy_from_slice(&DIAG_VERSION.to_le_bytes());
        dst[2] = clock_state_code(self.clock.state()) as u8;
        dst[3] = 0;
        dst[4..8].copy_from_slice(&self.rtt_ms().to_le_bytes());
        dst[8..16].copy_from_slice(&self.trail_q16(now_ms).to_le_bytes());
        dst[16..24].copy_from_slice(&self.error_q16(now_ms).to_le_bytes());
        dst[24..32].copy_from_slice(&self.tick.to_le_bytes());
        dst[32..40].copy_from_slice(&self.seq.to_le_bytes());
        dst[40..44].copy_from_slice(&(mythra_sim_clock::TRAIL_TARGET_TICKS as u32).to_le_bytes());
        dst[44..48].copy_from_slice(&(mythra_sim_clock::LAG_TICKS as u32).to_le_bytes());
        for (i, s) in self.stats.iter().enumerate() {
            let at = 48 + i * 8;
            dst[at..at + 8].copy_from_slice(&s.to_le_bytes());
        }
        DIAG_BYTES
    }

    /// The transport is up: greet the authority with what we already hold,
    /// then take our dog back into the park.
    pub fn connected<H: Host>(&mut self, h: &mut H, ticket: &[u8], now_ms: u64) -> u32 {
        if ticket.len() + 32 > OUT_CAP {
            return 1;
        }
        self.connected = true;
        self.conn = self.conn.wrapping_add(1).max(1);
        self.in_len = 0;
        self.resyncing = false;
        // The dog's presence belongs to the park, but our evidence for it
        // does not outlive the connection that carried it.
        self.presence = false;
        self.next_check_ms = now_ms + HELLO_MS;
        let n = wire::encode_hello(&mut bufs().out, self.seq, self.tick, ticket);
        h.send_stream(&bufs().out[..n]);
        h.emit(T_CONNECTED, ticket.len() as u64, 0);
        // A join still waiting for its answer is this connection's join
        // too. Minting a second asks the park to admit a dog it is already
        // being asked to admit, under an id it has never seen — two joins
        // on the wire, and the second one refused.
        if self.role == ROLE_PLAYER {
            match self.pending_kind(EV_JOIN) {
                Some(i) => self.resend(h, i, now_ms),
                None => {
                    self.send_intent(h, EV_JOIN, &self.my_dog.to_le_bytes(), now_ms);
                }
            }
        }
        0
    }

    pub fn disconnected(&mut self) {
        self.connected = false;
        self.resyncing = false;
        self.in_len = 0;
    }

    // ---- inbound ----

    pub fn on_stream<H: Host>(&mut self, h: &mut H, bytes: &[u8], now_ms: u64) {
        if self.in_len + bytes.len() > IN_CAP {
            self.teardown(h, R_STREAM_OVERFLOW);
            return;
        }
        bufs().inbuf[self.in_len..self.in_len + bytes.len()].copy_from_slice(bytes);
        self.in_len += bytes.len();
        loop {
            let bounds = wire::frame_bounds(&bufs().inbuf[..self.in_len]);
            let Ok(Some((kind, off, len, total))) = bounds else {
                if bounds.is_err() {
                    self.teardown(h, R_FRAMING);
                }
                return;
            };
            self.dispatch(h, kind, off, len, now_ms);
            bufs().inbuf.copy_within(total..self.in_len, 0);
            self.in_len -= total;
        }
    }

    /// Byte alignment is gone, and nothing sent on this stream can get it
    /// back. The host closes the transport; the redial is the repair.
    fn teardown<H: Host>(&mut self, h: &mut H, reason: u32) {
        self.in_len = 0;
        h.request(REQ_TEARDOWN, reason as u64);
    }

    fn dispatch<H: Host>(&mut self, h: &mut H, kind: u8, off: usize, len: usize, now_ms: u64) {
        match kind {
            wire::K_WELCOME => self.on_welcome(h, off, len, now_ms),
            wire::K_EVENT => self.on_event(h, off, len, now_ms),
            wire::K_REJECT => self.on_reject(h, off, len, now_ms),
            wire::K_SNAPSHOT => self.on_snapshot(h, off, len, now_ms),
            _ => {}
        }
    }

    fn on_welcome<H: Host>(&mut self, h: &mut H, off: usize, len: usize, now_ms: u64) {
        let Some((epoch, tick, hz, role, terrain)) =
            wire::parse_welcome(&bufs().inbuf[off..off + len])
                .map(|w| (w.epoch, w.tick, w.hz, w.role, w.terrain))
        else {
            return;
        };
        self.hz = hz.clamp(1, 1000) as u64;
        self.role = role as u32;
        // The park's rate paces this whole connection (rate changes only
        // happen while the authority is dark), and the welcome doubles as
        // the clock's first sample: same-ms echo, rtt unknown.
        self.clock.set_rate(self.hz);
        self.clock.sample(now_ms, now_ms, tick);
        h.emit(T_WELCOME, epoch as u64, self.hz | ((role as u64) << 32));
        if terrain != self.terrain_id {
            h.request(REQ_NEED_TERRAIN, terrain);
        }
    }

    fn on_event<H: Host>(&mut self, h: &mut H, off: usize, len: usize, now_ms: u64) {
        let mut ev = Ev::EMPTY;
        let mut oversize = false;
        {
            let Some(e) = wire::parse_event(&bufs().inbuf[off..off + len]) else {
                return;
            };
            if e.p.len() > EV_PAYLOAD_CAP {
                oversize = true;
            } else {
                ev.seq = e.seq;
                ev.tick = e.tick;
                ev.kind = e.kind;
                ev.intent = e.intent;
                ev.plen = e.p.len() as u16;
                ev.p[..e.p.len()].copy_from_slice(e.p);
            }
        }
        if oversize {
            self.request_resync(h, R_QUEUE_OVERFLOW, now_ms);
            return;
        }
        if ev.seq <= self.seq {
            return;
        }
        if self.queued_len == MAX_QUEUED {
            self.queued_len = 0;
            self.request_resync(h, R_QUEUE_OVERFLOW, now_ms);
            return;
        }
        let mut at = self.queued_len;
        while at > 0 && bufs().queued[at - 1].seq > ev.seq {
            at -= 1;
        }
        if at > 0 && bufs().queued[at - 1].seq == ev.seq {
            return;
        }
        bufs().queued.copy_within(at..self.queued_len, at + 1);
        bufs().queued[at] = ev;
        self.queued_len += 1;
        h.emit(T_EVENT_ARRIVED, ev.tick, self.tick);
    }

    fn on_reject<H: Host>(&mut self, h: &mut H, off: usize, len: usize, now_ms: u64) {
        let Some((intent, reason)) =
            wire::parse_reject(&bufs().inbuf[off..off + len]).map(|r| (r.intent, r.reason))
        else {
            return;
        };
        self.stats[STAT_REJECTS as usize - 1] += 1;
        let it = self.intent_take(intent);
        let kind = it.map(|i| i.kind).unwrap_or(0);
        // "Already present" describes the state we asked for, so it is
        // not news — and only a join can earn it, so it needs no pending
        // entry to be read that way. Copies arriving after the first has
        // consumed ours are just as unremarkable. It does confirm the dog
        // is in the park, which is the one fact the journal cannot always
        // deliver: a reconnecting player whose dog is already present is
        // answered with a snapshot, not with the join event that placed it.
        if reason == ERR_PRESENT {
            self.confirm_presence(h, PRESENCE_ALREADY_THERE, now_ms);
            return;
        }
        // Two rejects describe a world that already matches the intent:
        // a join for a dog the park still holds, and a boost transition
        // that already landed. Both ARE the requested state.
        if reason == ERR_NOOP && kind == EV_BOOST_SET {
            return;
        }
        h.emit(T_REJECT, reason as u64, kind as u64);
        // "Absent" on anything but a join means the park lost our dog (a
        // missed join, or a departure that raced the reconnect): re-join
        // and retry behind it, at most once per window.
        let rejoin = reason == ERR_ABSENT
            && self.role == ROLE_PLAYER
            && kind != 0
            && kind != EV_JOIN
            && (!self.auto_joined || now_ms.saturating_sub(self.last_auto_join_ms) > AUTO_JOIN_MS);
        if let (true, Some(it)) = (rejoin, it) {
            self.auto_joined = true;
            self.last_auto_join_ms = now_ms;
            h.emit(T_AUTO_REJOIN, reason as u64, kind as u64);
            self.send_intent(h, EV_JOIN, &self.my_dog.to_le_bytes(), now_ms);
            let p = it.p;
            self.send_intent(h, it.kind, &p[..it.plen as usize], now_ms);
        }
    }

    fn on_snapshot<H: Host>(&mut self, h: &mut H, off: usize, len: usize, now_ms: u64) {
        let Some((seq, tick, wh, terrain, zlen)) =
            wire::parse_snapshot(&bufs().inbuf[off..off + len])
                .map(|s| (s.seq, s.tick, s.wh, s.terrain, s.z.len()))
        else {
            return;
        };
        let zoff = off + wire::SNAPSHOT_HEADER;
        let n = {
            let b = bufs();
            let (src, dst) = (&b.inbuf[zoff..zoff + zlen], &mut b.hold);
            h.inflate(src, dst) as usize
        };
        if n == 0 || n > SNAP_CAP {
            h.emit(T_RESTORE_FAILED, 0, seq as u64);
            self.resyncing = false;
            self.retry_pending = true;
            return;
        }
        self.held_valid = true;
        self.held_seq = seq;
        self.held_tick = tick;
        self.held_wh = wh;
        self.held_terrain = terrain;
        self.held_len = n;
        // Restoring against the wrong world would replay a different park,
        // so the terrain lands first and the state waits for it.
        if terrain != self.terrain_id {
            h.request(REQ_NEED_TERRAIN, terrain);
            return;
        }
        self.land(h, now_ms, self.last_budget_us);
    }

    fn land<H: Host>(&mut self, h: &mut H, now_ms: u64, budget_us: u32) {
        if !self.held_valid {
            return;
        }
        self.held_valid = false;
        let was = self.tick;
        let code = h.park_restore(&bufs().hold[..self.held_len]);
        if code != 0 {
            h.emit(T_RESTORE_FAILED, code as u64, self.held_seq as u64);
            self.resyncing = false;
            // Wrong terrain is a contract violation, not a hiccup: latch it
            // and stop asking, because every snapshot the authority sends
            // will fail the same way until the terrain itself changes.
            if code == RESTORE_WRONG_TERRAIN {
                self.wrong_terrain = true;
            } else {
                self.retry_pending = true;
            }
            return;
        }
        self.seq = self.held_seq;
        // Safe because the authority queues a resync snapshot in-stream:
        // everything already waiting here arrived ahead of it and is
        // therefore folded into it. A snapshot delivered out of band would
        // strand every event newer than itself.
        self.queued_len = 0;
        self.recent_head = 0;
        self.recent_len = 0;
        self.ring_head = 0;
        self.ring_len = 0;
        // Every floor described a world that has just been replaced.
        self.repair_head = 0;
        self.repair_len = 0;
        self.hash_clear();
        self.tick = h.park_tick();
        let local = h.park_hash();
        self.hash_set(self.tick, local);
        // The ring must never be empty while a world is live. Cadence
        // entries only appear on the second, so without this a late event
        // arriving in the first second after a restore has no floor to
        // roll back to — and the only repair left is another snapshot,
        // which lands here and empties the ring again. That is a loop, and
        // it renders as a teleport every time round.
        let len = h.park_snapshot(&mut bufs().state) as usize;
        if len > 0 && len <= SNAP_CAP {
            self.ring_push(len);
        }
        self.resyncing = false;
        self.have_state = true;
        self.wrong_terrain = false;
        // Every outstanding check described the world this snapshot just
        // replaced.
        self.checks_len = 0;
        // Reconciled here rather than at the next pump: a snapshot lands
        // mid-frame, and a renderer reading the world between the restore
        // and the next pump would show the one it had before.
        self.corrected = true;
        self.clock.reset(self.held_tick, now_ms);
        // A snapshot is stamped where the authority stood when it took it,
        // which can be behind where this replica has already run to — and
        // presenting that would rewind a tick the viewer has passed. The
        // restored state is authoritative at its own tick, so walking it
        // forward is the same work the next few frames would have done
        // anyway. Bounded, because a restore from much further back is a
        // real correction and showing it beats grinding through it.
        let ceiling = was.min(self.tick + 2 * self.hz);
        if self.tick < ceiling {
            // Applying as we go rather than stepping past: events already
            // queued for these ticks would otherwise be late the moment we
            // arrived, and each would buy a rollback we caused ourselves.
            self.repairing = true;
            while self.tick < ceiling {
                self.step_once(h);
                self.apply_ready(h, now_ms, budget_us);
            }
            self.repairing = false;
        }
        h.emit(T_SNAPSHOT_RESTORED, self.seq as u64, self.tick);
        if local != self.held_wh {
            self.stats[STAT_MISMATCHES as usize - 1] += 1;
            h.emit(T_MISMATCH, self.tick, 0);
        }
        // Resend only what this connection has not already carried, and
        // nothing that is still waiting on the dog's presence.
        for i in 0..self.intents_len {
            let it = bufs().intents[i];
            if it.sent_conn == self.conn {
                continue;
            }
            if Self::needs_presence(it.kind) && self.role == ROLE_PLAYER && !self.presence {
                continue;
            }
            self.resend(h, i, now_ms);
        }
        if self.resync_on_landed {
            self.resync_on_landed = false;
            self.next_check_ms = now_ms + 2000 / self.hz;
        }
    }

    pub fn on_datagram<H: Host>(&mut self, h: &mut H, bytes: &[u8], now_ms: u64) {
        let Some(v) = wire::parse_verdict(bytes) else {
            return;
        };
        self.clock.sample(v.ct_ms, now_ms, v.now);
        let replaced = self.check_take(v.tick);
        let rtt = now_ms.saturating_sub(v.ct_ms);
        let ok = v.flags & wire::VERDICT_KNOWN != 0 && v.flags & wire::VERDICT_OK != 0;
        h.emit(T_VERDICT, ok as u64, rtt);
        // Backstop for an epoch_advance we never saw (attached mid-
        // boundary): the verdict names the authority's park module.
        let pw = wire::module_word(v.pw);
        if pw != 0 && self.park_pw != 0 && pw != self.park_pw {
            self.want_module(h, pw, now_ms);
        }
        if v.flags & wire::VERDICT_KNOWN == 0 {
            // "Unknown" answers two different questions: a tick that fell
            // out of the authority's ring, and one it has not stamped yet.
            // Only the first is staleness. A replica that has just
            // restored sits at the snapshot's tick — ahead of its own lag
            // target, holding — so its checks name ticks the server has
            // not reached, and counting those as strikes turns an honest
            // answer into a resync whose snapshot lands behind what is on
            // screen. This classification is a prerequisite for shrinking
            // LAG_TICKS: running hotter races the authority's present more
            // often, not less.
            if v.tick > v.now {
                return;
            }
            self.strike(h, R_CHECK_AGED_OUT, now_ms);
            return;
        }
        if ok {
            self.strikes = 0;
            return;
        }
        // A verdict answers the history the replica held when it asked. A
        // repair since then makes the answer true and useless: the hash it
        // disagrees with is one this replica has already replaced, and
        // striking on it resyncs a world that repaired itself correctly.
        if replaced {
            return;
        }
        self.stats[STAT_MISMATCHES as usize - 1] += 1;
        h.emit(T_MISMATCH, v.tick, 0);
        self.strike(h, R_HASH_MISMATCH, now_ms);
    }

    /// The terrain the host was asked for is (or is not) loaded. A held
    /// snapshot lands on the next pump, which carries time.
    pub fn terrain_ready<H: Host>(&mut self, _h: &mut H, id: u64, ok: bool) {
        if ok {
            self.terrain_id = id;
            // A different world is loaded: a snapshot can be worth asking
            // for again.
            self.wrong_terrain = false;
            if self.held_valid && self.held_terrain == id {
                self.land_pending = true;
            }
            return;
        }
        // A fetch that failed for terrain we are no longer waiting on says
        // nothing about the snapshot we are holding.
        if self.held_valid && self.held_terrain != id {
            return;
        }
        self.held_valid = false;
        self.resyncing = false;
        self.retry_pending = true;
    }

    /// The host has replaced the park instance. The new one holds no
    /// world, so the swap completes as a snapshot restore.
    pub fn module_swapped<H: Host>(&mut self, h: &mut H, pw: u32) {
        self.park_pw = pw;
        self.module_latch = false;
        h.emit(T_MODULE_SWAPPED, pw as u64, 0);
        if !self.have_state {
            return;
        }
        self.have_state = false;
        self.held_valid = false;
        self.queued_len = 0;
        self.recent_head = 0;
        self.recent_len = 0;
        self.ring_head = 0;
        self.ring_len = 0;
        // Every floor described a world that has just been replaced.
        self.repair_head = 0;
        self.repair_len = 0;
        self.hash_clear();
        self.resyncing = false;
        self.deferred_resync = R_MODULE_SWAPPED;
    }

    // ---- outbound intents ----

    pub fn intent_join<H: Host>(&mut self, h: &mut H, now_ms: u64) -> u64 {
        self.send_intent(h, EV_JOIN, &self.my_dog.to_le_bytes(), now_ms)
    }

    pub fn intent_check_in<H: Host>(&mut self, h: &mut H, now_ms: u64) -> u64 {
        self.send_intent(h, EV_CHECK_IN, &self.my_dog.to_le_bytes(), now_ms)
    }

    pub fn intent_move_to<H: Host>(&mut self, h: &mut H, node: u32, now_ms: u64) -> u64 {
        let mut p = [0u8; 10];
        p[..8].copy_from_slice(&self.my_dog.to_le_bytes());
        p[8..10].copy_from_slice(&(node as u16).to_le_bytes());
        self.send_intent(h, EV_MOVE_TO, &p, now_ms)
    }

    pub fn intent_boost<H: Host>(&mut self, h: &mut H, on: bool, now_ms: u64) -> u64 {
        let mut p = [0u8; 9];
        p[..8].copy_from_slice(&self.my_dog.to_le_bytes());
        p[8] = on as u8;
        self.send_intent(h, EV_BOOST_SET, &p, now_ms)
    }

    fn send_intent<H: Host>(&mut self, h: &mut H, kind: u16, payload: &[u8], now_ms: u64) -> u64 {
        self.counter = self.counter.wrapping_add(1);
        let id = ((self.nonce as u64) << 32) | self.counter as u64;
        if self.intents_len == MAX_INTENTS {
            let evicted = bufs().intents[0];
            h.emit(
                T_INTENT_DROPPED,
                (DROP_OVERFLOW << 16) | evicted.kind as u64,
                held_ms(&evicted, now_ms),
            );
            bufs().intents.copy_within(1..MAX_INTENTS, 0);
            self.intents_len -= 1;
        }
        let mut it = Intent {
            id,
            kind,
            plen: payload.len() as u16,
            sent_conn: 0,
            sent_ms: 0,
            resends: 0,
            p: [0; EV_PAYLOAD_CAP],
        };
        it.p[..payload.len()].copy_from_slice(payload);
        bufs().intents[self.intents_len] = it;
        self.intents_len += 1;
        // An intent that needs a dog in the park waits until the park says
        // there is one. Racing the join earns an ABSENT refusal, which
        // triggers a re-join and a retry — a spiral out of one tap.
        if self.role == ROLE_PLAYER && !self.presence && Self::needs_presence(kind) {
            return id;
        }
        let n = wire::encode_intent(&mut bufs().out, id, kind, payload);
        h.send_stream(&bufs().out[..n]);
        bufs().intents[self.intents_len - 1].sent_conn = self.conn;
        bufs().intents[self.intents_len - 1].sent_ms = now_ms;
        h.emit(T_INTENT_SENT, kind as u64, id);
        id
    }

    // ---- the local tick loop ----

    /// One frame: obey the clock's directive, apply what the journal has
    /// delivered, and keep the check cadence. The host executes nothing
    /// else.
    ///
    /// `budget_us` of 0 means observe rather than step: the clock still
    /// takes its frame, so it escalates and can still demand a snapshot,
    /// ready events still apply, and checks still go out — but neither
    /// the world advances a tick. That is what a starved replica looks
    /// like,
    /// which is exactly what makes it a usable freeze for dev loops. A
    /// zero budget also buys no rollback, so a late event resyncs instead
    /// of rewinding — academic in practice, since a starved replica runs
    /// behind the authority and every event it receives is a future one.
    pub fn pump<H: Host>(&mut self, h: &mut H, now_ms: u64, budget_us: u32) -> u32 {
        self.last_budget_us = budget_us.max(1);
        if self.retry_pending {
            self.retry_pending = false;
            self.retry_at_ms = now_ms + RETRY_MS;
        }
        // A resync nobody answered is a session that has gone quiet: the
        // seq gap never closes, so no event applies again. Ask once more,
        // on the clock's own re-request cadence.
        if self.resyncing && now_ms.saturating_sub(self.resync_at_ms) >= RESYNC_REASK_MS {
            let again = self.resync_reason;
            self.resyncing = false;
            self.request_resync(h, again, now_ms);
        }
        if self.retry_at_ms != 0 && now_ms >= self.retry_at_ms {
            self.retry_at_ms = 0;
            self.request_resync(h, R_TERRAIN_FETCH, now_ms);
        }
        if self.deferred_resync != 0 {
            let reason = self.deferred_resync;
            self.deferred_resync = 0;
            self.request_resync(h, reason, now_ms);
        }
        if self.land_pending {
            self.land_pending = false;
            self.land(h, now_ms, budget_us);
        }
        let mut stepped = false;
        if self.have_state {
            let d = self.clock.frame(now_ms, self.tick, budget_us);
            if d & F_SNAPSHOT != 0 {
                self.request_resync(h, R_CLOCK, now_ms);
            }
            // A zero budget observes without stepping. The clock still ran
            // its frame above, so it keeps escalating and can still demand
            // a snapshot; the guard has to live here because the Locked
            // branch draws its steps from the fractional accumulator and
            // never consults the budget.
            let mut steps = if budget_us == 0 { 0 } else { d & 0xFFFF };
            stepped = steps > 0;
            while steps > 0 {
                self.step_once(h);
                self.apply_ready(h, now_ms, budget_us);
                steps -= 1;
            }
            self.apply_ready(h, now_ms, budget_us);
            if self.visible && self.connected && now_ms >= self.next_check_ms {
                self.send_check(h, now_ms);
                self.next_check_ms = now_ms + self.check_ms;
            }
        }
        let st = clock_state_code(self.clock.state());
        if st != self.clock_state {
            self.clock_state = st;
            let err = self.clock.error_q16(now_ms, self.tick) >> 16;
            h.emit(T_CLOCK_STATE, st as u64, err as u64);
        }
        let bit = |on: bool, flag: u32| if on { flag } else { 0 };
        bit(self.have_state, S_HAVE_STATE)
            | bit(self.resyncing, S_RESYNCING)
            | bit(stepped, S_STEPPED)
            | bit(self.held_valid, S_WANT_TERRAIN)
            | bit(self.module_latch, S_WANT_MODULE)
            | bit(mem::take(&mut self.corrected), S_CORRECTED)
            | (st << S_CLOCK_SHIFT)
    }

    fn step_once<H: Host>(&mut self, h: &mut H) {
        h.park_step();
        self.tick = h.park_tick();
        let hash = h.park_hash();
        // The ring entry is the state at *entry* to this tick: the events
        // stamped for it have not been applied yet, and re-hashing after
        // they are would poison exactly the entry a same-tick check
        // samples.
        self.hash_set(self.tick, hash);
        let cadence = self.tick % self.hz == 0;
        if cadence || self.repairing {
            let len = h.park_snapshot(&mut bufs().state) as usize;
            if len > 0 && len <= SNAP_CAP {
                if self.repairing {
                    self.repair_push(len);
                }
                if cadence {
                    self.ring_push(len);
                }
            }
        }
        if cadence && !self.replaying {
            self.prune_recent();
        }
    }

    /// Applies queued events at their exact tick, in dense seq order. A
    /// gap waits; a late event rolls back through the snapshot ring and
    /// replays; deeper than the ring — or than this frame's budget — is a
    /// resync. A rollback always ends where it started: the rewind and the
    /// replay both happen inside this call, so no frame ever renders the
    /// past.
    fn apply_ready<H: Host>(&mut self, h: &mut H, now_ms: u64, budget_us: u32) {
        self.apply_and_resume(h, now_ms, budget_us);
        // The repair is over however this ended: the floors exist for the
        // walk back, and a resync that gave up on it is still an end.
        self.repairing = false;
    }

    fn apply_and_resume<H: Host>(&mut self, h: &mut H, now_ms: u64, budget_us: u32) {
        // Where the replica stood before any rewind: the tick this call
        // owes the renderer by the time it returns.
        let mut resume = 0;
        loop {
            while self.queued_len > 0 && bufs().queued[0].seq <= self.seq {
                self.queued_drop_front();
            }
            while self.queued_len > 0 && bufs().queued[0].seq == self.seq + 1 {
                let e = bufs().queued[0];
                if e.tick > self.tick {
                    break;
                }
                if e.tick < self.tick {
                    let was = self.tick;
                    if !self.rollback_to(h, e.tick, budget_us) {
                        self.request_resync(h, R_LATE_EVENT, now_ms);
                        return;
                    }
                    resume = resume.max(was);
                }
                self.queued_drop_front();
                let code = self.apply(h, e.kind, e.payload());
                if code != 0 {
                    self.request_resync(h, R_EVENT_REJECTED, now_ms);
                    return;
                }
                self.seq = e.seq;
                // An event is not always a pure edit on top of the tick it
                // lands in. `terrain_set` relocates dogs, which is rendered
                // motion no step paid for and the only apply that produces
                // any; everything else edits fields the renderer reads without
                // moving anything.
                if e.kind == EV_TERRAIN_SET {
                    self.corrected = true;
                }
                // `clock_skip` moves the park's clock rather than its state, so
                // the tick is read back rather than assumed — and the model of
                // the authority moves with it. The authority journaled this
                // skip from its own tick, so both worlds are at the
                // destination; without the reset the clock sees a replica
                // hours ahead of where it believes the authority stands and
                // demands a snapshot to repair a world that is already right.
                let park_tick = h.park_tick();
                if park_tick != self.tick {
                    self.tick = park_tick;
                    self.clock.reset(park_tick, now_ms);
                }
                self.stats[STAT_EVENTS as usize - 1] += 1;
                if let Some(it) = self.intent_take(e.intent) {
                    h.emit(
                        T_INTENT_ANSWERED,
                        ((it.resends as u64) << 16) | it.kind as u64,
                        now_ms.saturating_sub(it.sent_ms),
                    );
                }
                self.recent_push(e);
                // Presence is the journal's to state: our dog is in the park
                // when the journal says a join placed it, whoever's intent id
                // that event happens to carry.
                if e.kind == EV_JOIN && e.plen == 8 && !self.presence {
                    let mut id = [0u8; 8];
                    id.copy_from_slice(&e.p[..8]);
                    if u64::from_le_bytes(id) == self.my_dog {
                        self.confirm_presence(h, PRESENCE_JOURNAL, now_ms);
                    }
                }
                h.emit(T_EVENT_APPLIED, e.seq as u64, e.tick);
                if e.kind == EV_EPOCH_ADVANCE && e.plen == 12 {
                    let mut b = [0u8; 8];
                    b.copy_from_slice(&e.p[4..12]);
                    self.want_module(h, u64::from_le_bytes(b) as u32, now_ms);
                }
            }
            if self.tick >= resume {
                return;
            }
            // The walk back to the present applies as it goes. Stepping past a
            // queued event stamped inside the walk makes it late the moment we
            // arrive, and it buys a rollback we caused ourselves — which walks
            // again, and strands the next one. One late event becomes a train
            // of them, all of them ours.
            self.step_once(h);
        }
    }

    /// Rewinds to the newest ring entry at or before `tick`, replays the
    /// events since it, and stops exactly at `tick` so the late event can
    /// apply where it belongs. The caller walks the replica back to the
    /// present before it returns, so the rewind is never rendered.
    ///
    /// False means the repair is out of reach — no ring entry that deep,
    /// or more steps than this frame's budget can pay for — and only a
    /// snapshot will do. Refusing beats a half-finished rewind: the screen
    /// would show the past and then jump.
    ///
    /// Load-bearing assumption: dogs do not interact. A dog's step reads
    /// only its own state, the terrain, and `det_rand(seed, tick, id)`, so
    /// replaying an event that moves one dog reproduces every other dog's
    /// trajectory bit-identically and nothing on screen twitches but the
    /// actor. The day dog-dog interaction ships, that stops being true and
    /// this becomes a visible correction for bystanders.
    fn rollback_to<H: Host>(&mut self, h: &mut H, tick: u64, budget_us: u32) -> bool {
        // The newest floor at or before the target. Rewind distance is
        // repair cost, not something anyone sees: the pump budget bounds
        // it and the resync hatch catches the rest, so the ring stays one
        // entry per second rather than paying a per-tick copy forever.
        // By index — a Snap is most of a 64 KiB buffer, never moved.
        let mut best: Option<(bool, usize)> = None;
        let mut best_tick = 0;
        for i in 0..self.repair_len {
            let idx = self.repair_idx(i);
            let t = bufs().repair[idx].tick;
            if t <= tick && (best.is_none() || t > best_tick) {
                best = Some((true, idx));
                best_tick = t;
            }
        }
        for i in 0..self.ring_len {
            let idx = self.ring_idx(i);
            let t = bufs().ring[idx].tick;
            if t <= tick && (best.is_none() || t > best_tick) {
                best = Some((false, idx));
                best_tick = t;
            }
        }
        {
            let Some((from_repair, idx)) = best else {
                return false;
            };
            let was = self.tick;
            let entry = if from_repair {
                bufs().repair[idx]
            } else {
                bufs().ring[idx]
            };
            let (stick, sseq, slen) = (entry.tick, entry.seq, entry.len);
            // Every tick between the entry and the present gets stepped
            // exactly once on the way back out. If this frame cannot pay
            // for all of them, refuse the whole repair — a rewind the
            // budget cannot finish is a rewind the user watches.
            if was.saturating_sub(stick) > (budget_us / STEP_COST_US) as u64 {
                return false;
            }
            // The authority stamps ticks monotonically with seq, so every
            // event to replay belongs at or before `tick`. One that does
            // not would be silently dropped by this replay: refuse.
            for k in 0..self.recent_len {
                let e = self.recent_get(k);
                if e.seq > sseq && e.tick > tick {
                    return false;
                }
            }
            let restored = if from_repair {
                h.park_restore(&bufs().repair[idx].state[..slen])
            } else {
                h.park_restore(&bufs().ring[idx].state[..slen])
            };
            if restored != 0 {
                return false;
            }
            self.seq = sseq;
            self.tick = h.park_tick();
            // A repair moves the world under whatever has been drawn, so
            // the host is told to absorb it the same way it absorbs a
            // restore.
            self.corrected = true;
            // Every tick from the late event onward is about to be
            // re-derived, so any check still waiting on one of them
            // described a history this replica is replacing right now.
            for i in 0..self.checks_len {
                if self.checks[i].tick >= tick {
                    self.checks[i].replaced = true;
                }
            }
            self.stats[STAT_ROLLBACKS as usize - 1] += 1;
            h.emit(
                T_ROLLBACK,
                was,
                (was.saturating_sub(tick) << 32) | was.saturating_sub(stick),
            );
            self.replaying = true;
            self.repairing = true;
            for k in 0..self.recent_len {
                let e = self.recent_get(k);
                if e.seq <= sseq {
                    continue;
                }
                while self.tick < e.tick {
                    self.step_once(h);
                }
                h.emit(T_REPLAYED, e.seq as u64, e.tick);
                if self.apply(h, e.kind, e.payload()) == 0 {
                    self.seq = e.seq;
                    // Replayed history moves the tick exactly where it
                    // moved it the first time: stepping from an assumed
                    // tick past a replayed `clock_skip` would land this
                    // replica somewhere the authority never stood.
                    self.tick = h.park_tick();
                } else {
                    // The replay could not rebuild what this replica had
                    // already applied. Finish the walk so time keeps
                    // moving, then ask for a world that is true — silence
                    // here is a divergence nothing else in the protocol
                    // can find.
                    self.deferred_resync = R_REPLAY_REFUSED;
                }
            }
            while self.tick < tick {
                self.step_once(h);
            }
            self.replaying = false;
            true
        }
    }

    fn apply<H: Host>(&mut self, h: &mut H, kind: u16, payload: &[u8]) -> u32 {
        let mut buf = [0u8; 2 + EV_PAYLOAD_CAP];
        buf[..2].copy_from_slice(&kind.to_le_bytes());
        buf[2..2 + payload.len()].copy_from_slice(payload);
        h.park_apply(&buf[..2 + payload.len()])
    }

    // ---- checks, strikes, resync ----

    fn send_check<H: Host>(&mut self, h: &mut H, now_ms: u64) {
        let Some(wh) = self.hash_get(self.tick) else {
            return;
        };
        let n = wire::encode_check(&mut bufs().out, self.tick, wh, now_ms);
        h.send_datagram(&bufs().out[..n]);
        if self.checks_len == MAX_CHECKS {
            self.checks.copy_within(1..MAX_CHECKS, 0);
            self.checks_len -= 1;
        }
        self.checks[self.checks_len] = Check {
            tick: self.tick,
            replaced: false,
        };
        self.checks_len += 1;
        self.stats[STAT_CHECKS as usize - 1] += 1;
        h.emit(T_CHECK_SENT, self.tick, 0);
    }

    fn strike<H: Host>(&mut self, h: &mut H, reason: u32, now_ms: u64) {
        self.strikes += 1;
        if self.strikes >= STRIKES_TO_RESYNC {
            self.strikes = 0;
            // Divergence is real, but while the last snapshot was refused
            // for its terrain another one repairs nothing — it would just
            // fail again at the check cadence, forever.
            if self.wrong_terrain {
                return;
            }
            self.request_resync(h, reason, now_ms);
        }
    }

    fn request_resync<H: Host>(&mut self, h: &mut H, reason: u32, _now_ms: u64) {
        if self.resyncing || !self.connected {
            return;
        }
        self.resyncing = true;
        self.resync_at_ms = _now_ms;
        self.resync_reason = reason;
        self.resync_on_landed = true;
        self.stats[STAT_RESYNCS as usize - 1] += 1;
        h.emit(T_RESYNC_REQUESTED, reason as u64, self.seq as u64);
        h.request(REQ_RESYNC_WANTED, reason as u64);
        let n = wire::encode_resync(&mut bufs().out, self.seq);
        h.send_stream(&bufs().out[..n]);
    }

    fn want_module<H: Host>(&mut self, h: &mut H, pw: u32, now_ms: u64) {
        if self.module_latch || pw == 0 || pw == self.park_pw {
            return;
        }
        self.module_latch = true;
        h.emit(T_MODULE_SWAP_WANTED, pw as u64, 0);
        h.request(REQ_NEED_MODULE, pw as u64);
        self.request_resync(h, R_MODULE_EPOCH, now_ms);
    }

    // ---- fixed-capacity collections ----

    fn hash_set(&mut self, tick: u64, hash: u64) {
        let i = (tick % HASH_RING as u64) as usize;
        bufs().hashes[i] = HashEntry {
            tick,
            hash,
            valid: true,
        };
    }

    fn hash_get(&self, tick: u64) -> Option<u64> {
        let e = bufs().hashes[(tick % HASH_RING as u64) as usize];
        (e.valid && e.tick == tick).then_some(e.hash)
    }

    fn hash_clear(&mut self) {
        for e in bufs().hashes.iter_mut() {
            e.valid = false;
        }
    }

    fn ring_idx(&self, i: usize) -> usize {
        (self.ring_head + i) % RING_DEPTH
    }

    fn repair_idx(&self, i: usize) -> usize {
        (self.repair_head + i) % REPAIR_DEPTH
    }

    fn repair_push(&mut self, len: usize) {
        if self.repair_len == REPAIR_DEPTH {
            self.repair_head = (self.repair_head + 1) % REPAIR_DEPTH;
            self.repair_len -= 1;
        }
        let idx = self.repair_idx(self.repair_len);
        let (tick, seq) = (self.tick, self.seq);
        let b = bufs();
        b.repair[idx].tick = tick;
        b.repair[idx].seq = seq;
        b.repair[idx].len = len;
        let (dst, src) = (&mut b.repair[idx].state[..len], &b.state[..len]);
        dst.copy_from_slice(src);
        self.repair_len += 1;
    }

    fn ring_push(&mut self, len: usize) {
        if self.ring_len == RING_DEPTH {
            self.ring_head = (self.ring_head + 1) % RING_DEPTH;
            self.ring_len -= 1;
        }
        let idx = self.ring_idx(self.ring_len);
        let (tick, seq) = (self.tick, self.seq);
        let b = bufs();
        b.ring[idx].tick = tick;
        b.ring[idx].seq = seq;
        b.ring[idx].len = len;
        let (dst, src) = (&mut b.ring[idx].state[..len], &b.state[..len]);
        dst.copy_from_slice(src);
        self.ring_len += 1;
    }

    fn prune_recent(&mut self) {
        let floor = if self.ring_len > 0 {
            bufs().ring[self.ring_idx(0)].tick
        } else {
            0
        };
        while self.recent_len > 0 && self.recent_get(0).tick < floor {
            self.recent_head = (self.recent_head + 1) % MAX_RECENT;
            self.recent_len -= 1;
        }
    }

    fn recent_get(&self, i: usize) -> Ev {
        bufs().recent[(self.recent_head + i) % MAX_RECENT]
    }

    fn recent_push(&mut self, e: Ev) {
        if self.recent_len == MAX_RECENT {
            self.recent_head = (self.recent_head + 1) % MAX_RECENT;
            self.recent_len -= 1;
        }
        let i = (self.recent_head + self.recent_len) % MAX_RECENT;
        bufs().recent[i] = e;
        self.recent_len += 1;
    }

    fn queued_drop_front(&mut self) {
        bufs().queued.copy_within(1..self.queued_len, 0);
        self.queued_len -= 1;
    }

    /// Does an intent of this kind still await an answer?
    fn pending_kind(&self, kind: u16) -> Option<usize> {
        (0..self.intents_len).find(|&i| bufs().intents[i].kind == kind)
    }

    /// Puts a pending intent back on the wire under its original id — the
    /// authority dedupes by that id, so a resend can never double-apply.
    fn resend<H: Host>(&mut self, h: &mut H, i: usize, now_ms: u64) {
        let it = bufs().intents[i];
        let n = wire::encode_intent(&mut bufs().out, it.id, it.kind, it.payload());
        h.send_stream(&bufs().out[..n]);
        // A first wire write — an intent held until presence confirmed —
        // IS the send, and the action's latency clock starts here; a
        // repeat under the same id is a resend and only counts. Sent-ness
        // is `sent_conn`, never `sent_ms == 0`: a host clock can read 0.
        let first = it.sent_conn == 0;
        bufs().intents[i].sent_conn = self.conn;
        if first {
            bufs().intents[i].sent_ms = now_ms;
            h.emit(T_INTENT_SENT, it.kind as u64, it.id);
        } else {
            bufs().intents[i].resends = it.resends.saturating_add(1);
            h.emit(
                T_INTENT_RESENT,
                it.kind as u64,
                bufs().intents[i].resends as u64,
            );
        }
    }

    /// Intents the park refuses unless our dog is already standing in it.
    fn needs_presence(kind: u16) -> bool {
        matches!(kind, EV_CHECK_IN | EV_MOVE_TO | EV_BOOST_SET)
    }

    /// The park has our dog: whatever was waiting on that can go now.
    fn confirm_presence<H: Host>(&mut self, h: &mut H, source: u64, now_ms: u64) {
        if self.presence {
            return;
        }
        self.presence = true;
        h.emit(T_PRESENCE, source, self.tick);
        for i in 0..self.intents_len {
            if bufs().intents[i].sent_conn != self.conn {
                self.resend(h, i, now_ms);
            }
        }
    }

    /// Retires the outstanding check for `tick`, answering whether a
    /// repair rewrote that tick while the verdict was in flight.
    fn check_take(&mut self, tick: u64) -> bool {
        let mut i = 0;
        while i < self.checks_len {
            if self.checks[i].tick == tick {
                let replaced = self.checks[i].replaced;
                self.checks.copy_within(i + 1..self.checks_len, i);
                self.checks_len -= 1;
                return replaced;
            }
            i += 1;
        }
        false
    }

    fn intent_take(&mut self, id: u64) -> Option<Intent> {
        if id == 0 {
            return None;
        }
        let mut i = 0;
        while i < self.intents_len {
            if bufs().intents[i].id == id {
                let it = bufs().intents[i];
                bufs().intents.copy_within(i + 1..self.intents_len, i);
                self.intents_len -= 1;
                return Some(it);
            }
            i += 1;
        }
        None
    }
}

/// How long a discarded intent had been waiting: 0 for one that never
/// reached the wire (there is no send to measure from). `sent_conn` is
/// the was-it-sent discriminator, never `sent_ms` — a host whose clock
/// reads 0 at the first send would otherwise be misclassified, and the
/// deterministic rigs boot at exactly that clock.
fn held_ms(it: &Intent, now_ms: u64) -> u64 {
    if it.sent_conn == 0 {
        return 0;
    }
    now_ms.saturating_sub(it.sent_ms)
}

fn clock_state_code(s: State) -> u32 {
    match s {
        State::Acquiring => 0,
        State::Locked => 1,
        State::FastForward => 2,
        State::SnapshotRequired => 3,
    }
}

#[cfg(test)]
mod tests;

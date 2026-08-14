//! The replica session core: everything between the wire and the park
//! module that decides *what the world is*. Frame codec, seq-dense event
//! ordering, temporal rollback, resync and strike policy, check cadence,
//! intent identity and prediction, reject policy, and the module-swap
//! choreography all live here, so every host — browser, app, load bot —
//! inherits identical netcode behavior instead of reimplementing it.
//!
//! Pure by construction: time is always a parameter, entropy arrives as
//! the `init` nonce, and every effect leaves through the `Host` trait
//! (park verbs by slot, transport writes, inflate, host requests,
//! telemetry). No allocator, no floats, no clocks.
//!
//! Two park instances, addressed by slot:
//!   slot 0 "journal replica"  journal events only, in dense seq order at
//!                             their tick; snapshot ring (depth 10, one
//!                             per second) + recent-event replay backs
//!                             rollback. Hash ring and checks read here.
//!   slot 1 "presented"        slot 0's state with this client's pending
//!                             intents applied on top. Rebuilt whenever
//!                             slot 0 mutates outside lockstep stepping
//!                             or the pending set changes. Renderers and
//!                             the HUD read here.
//!
//! Wire protocol v4 (little-endian scalars; the QUIC varint length prefix
//! is big-endian per RFC 9000 §16) is in [`wire`].
#![cfg_attr(not(test), no_std)]

use mythra_sim_clock::{Clock, F_SNAPSHOT, State};

pub mod wire;

/// Park verb slots.
pub const SLOT_JOURNAL: u32 = 0;
pub const SLOT_PRESENTED: u32 = 1;

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

/// `session_pump` status bits.
pub const S_HAVE_STATE: u32 = 1;
pub const S_RESYNCING: u32 = 1 << 1;
pub const S_STEPPED: u32 = 1 << 2;
pub const S_WANT_TERRAIN: u32 = 1 << 3;
pub const S_WANT_MODULE: u32 = 1 << 4;
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
const EV_BOOST_SET: u16 = 8;
const ERR_PRESENT: u32 = 2;
const ERR_ABSENT: u32 = 3;
const ERR_NOOP: u32 = 10;
/// `sim_restore` refusing a snapshot taken on other terrain.
const RESTORE_WRONG_TERRAIN: u32 = 4;

/// One snapshot per second of rollback depth.
const RING_DEPTH: usize = 10;
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
const EV_PAYLOAD_CAP: usize = 64;
const HELLO_MS: u64 = 1500;
const RETRY_MS: u64 = 3000;
const AUTO_JOIN_MS: u64 = 5000;
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
    /// each other's events and corrupting the overlay.
    id: u64,
    kind: u16,
    plen: u16,
    p: [u8; EV_PAYLOAD_CAP],
}

impl Intent {
    const EMPTY: Intent = Intent {
        id: 0,
        kind: 0,
        plen: 0,
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

/// Everything the core asks of its embedder. Slot-addressed park verbs
/// operate on the host's two park instances; the host copies bytes between
/// the two wasm memories.
pub trait Host {
    fn park_apply(&mut self, slot: u32, ev: &[u8]) -> u32;
    fn park_step(&mut self, slot: u32);
    fn park_snapshot(&mut self, slot: u32, dst: &mut [u8]) -> u32;
    fn park_restore(&mut self, slot: u32, state: &[u8]) -> u32;
    fn park_hash(&mut self, slot: u32) -> u64;
    fn park_tick(&mut self, slot: u32) -> u64;
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
    have_state: bool,
    visible: bool,
    dirty: bool,
    replaying: bool,
    resyncing: bool,
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

    next_check_ms: u64,
    auto_joined: bool,
    last_auto_join_ms: u64,
    clock_state: u32,
    stats: [u64; 8],

    in_len: usize,
    ring_head: usize,
    ring_len: usize,
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
        have_state: false,
        visible: true,
        dirty: false,
        replaying: false,
        resyncing: false,
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

        next_check_ms: 0,
        auto_joined: false,
        last_auto_join_ms: 0,
        clock_state: 0,
        stats: [0; 8],

        in_len: 0,
        ring_head: 0,
        ring_len: 0,
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
        self.have_state = false;
        self.visible = true;
        self.dirty = false;
        self.replaying = false;
        self.resyncing = false;
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
        self.next_check_ms = 0;
        self.auto_joined = false;
        self.last_auto_join_ms = 0;
        self.clock_state = 0;
        self.stats = [0; 8];
        self.in_len = 0;
        self.ring_head = 0;
        self.ring_len = 0;
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
    pub fn reidentify(&mut self, my_dog: u64, role: u32, nonce: u32, _now_ms: u64) {
        self.my_dog = my_dog;
        self.role = role;
        self.nonce = nonce;
        self.counter = 0;
        self.intents_len = 0;
        self.auto_joined = false;
        self.last_auto_join_ms = 0;
        self.dirty = true;
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

    pub fn stat(&self, kind: u32) -> u64 {
        match kind {
            1..=6 => self.stats[kind as usize - 1],
            _ => 0,
        }
    }

    /// The transport is up: greet the authority with what we already hold,
    /// then take our dog back into the park.
    pub fn connected<H: Host>(&mut self, h: &mut H, ticket: &[u8], now_ms: u64) -> u32 {
        if ticket.len() + 32 > OUT_CAP {
            return 1;
        }
        self.connected = true;
        self.in_len = 0;
        self.resyncing = false;
        self.next_check_ms = now_ms + HELLO_MS;
        let n = wire::encode_hello(&mut bufs().out, self.seq, self.tick, ticket);
        h.send_stream(&bufs().out[..n]);
        h.emit(T_CONNECTED, ticket.len() as u64, 0);
        if self.role == ROLE_PLAYER {
            self.send_intent(h, EV_JOIN, &self.my_dog.to_le_bytes(), now_ms);
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
    }

    fn on_reject<H: Host>(&mut self, h: &mut H, off: usize, len: usize, now_ms: u64) {
        let Some((intent, reason)) =
            wire::parse_reject(&bufs().inbuf[off..off + len]).map(|r| (r.intent, r.reason))
        else {
            return;
        };
        self.stats[STAT_REJECTS as usize - 1] += 1;
        let it = self.intent_take(intent);
        if it.is_some() {
            // The overlay is predicting something the authority refused.
            self.dirty = true;
        }
        let kind = it.map(|i| i.kind).unwrap_or(0);
        // Two rejects describe a world that already matches the intent:
        // a join for a dog the park still holds, and a boost transition
        // that already landed. Both ARE the requested state.
        if (reason == ERR_PRESENT && kind == EV_JOIN)
            || (reason == ERR_NOOP && kind == EV_BOOST_SET)
        {
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
        self.land(h, now_ms);
    }

    fn land<H: Host>(&mut self, h: &mut H, now_ms: u64) {
        if !self.held_valid {
            return;
        }
        self.held_valid = false;
        let code = h.park_restore(SLOT_JOURNAL, &bufs().hold[..self.held_len]);
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
        self.queued_len = 0;
        self.recent_head = 0;
        self.recent_len = 0;
        self.ring_head = 0;
        self.ring_len = 0;
        self.hash_clear();
        self.tick = h.park_tick(SLOT_JOURNAL);
        let local = h.park_hash(SLOT_JOURNAL);
        self.hash_set(self.tick, local);
        self.resyncing = false;
        self.have_state = true;
        self.wrong_terrain = false;
        self.dirty = true;
        self.clock.reset(self.held_tick, now_ms);
        h.emit(T_SNAPSHOT_RESTORED, self.seq as u64, self.tick);
        if local != self.held_wh {
            self.stats[STAT_MISMATCHES as usize - 1] += 1;
            h.emit(T_MISMATCH, self.tick, 0);
        }
        for i in 0..self.intents_len {
            let it = bufs().intents[i];
            let n = wire::encode_intent(&mut bufs().out, it.id, it.kind, it.payload());
            h.send_stream(&bufs().out[..n]);
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
            self.strike(h, R_CHECK_AGED_OUT, now_ms);
            return;
        }
        if ok {
            self.strikes = 0;
            return;
        }
        self.stats[STAT_MISMATCHES as usize - 1] += 1;
        h.emit(T_MISMATCH, v.tick, 0);
        self.strike(h, R_HASH_MISMATCH, now_ms);
    }

    /// The terrain the host was asked for is (or is not) loaded into both
    /// slots. A held snapshot lands on the next pump, which carries time.
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

    /// The host has replaced both slots' park instances. The new instances
    /// hold no world, so the swap completes as a snapshot restore.
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

    fn send_intent<H: Host>(&mut self, h: &mut H, kind: u16, payload: &[u8], _now_ms: u64) -> u64 {
        self.counter = self.counter.wrapping_add(1);
        let id = ((self.nonce as u64) << 32) | self.counter as u64;
        if self.intents_len == MAX_INTENTS {
            bufs().intents.copy_within(1..MAX_INTENTS, 0);
            self.intents_len -= 1;
        }
        let mut it = Intent {
            id,
            kind,
            plen: payload.len() as u16,
            p: [0; EV_PAYLOAD_CAP],
        };
        it.p[..payload.len()].copy_from_slice(payload);
        bufs().intents[self.intents_len] = it;
        self.intents_len += 1;
        self.dirty = true;
        let n = wire::encode_intent(&mut bufs().out, id, kind, payload);
        h.send_stream(&bufs().out[..n]);
        h.emit(T_INTENT_SENT, kind as u64, id);
        id
    }

    // ---- the local tick loop ----

    /// One frame: obey the clock's directive, apply what the journal has
    /// delivered, keep the check cadence, and re-derive the presented
    /// world. The host executes nothing else.
    ///
    /// `budget_us` of 0 means observe rather than step: the clock still
    /// takes its frame, so it escalates and can still demand a snapshot,
    /// ready events still apply, and checks still go out — but neither
    /// slot advances a tick. That is what a starved replica looks like,
    /// which is exactly what makes it a usable freeze for dev loops.
    /// Rollback replay is driven by arriving events rather than by the
    /// clock, so a late event still repairs itself under a zero budget.
    pub fn pump<H: Host>(&mut self, h: &mut H, now_ms: u64, budget_us: u32) -> u32 {
        if self.retry_pending {
            self.retry_pending = false;
            self.retry_at_ms = now_ms + RETRY_MS;
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
            self.land(h, now_ms);
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
                self.apply_ready(h, now_ms);
                steps -= 1;
            }
            self.apply_ready(h, now_ms);
            if self.visible && self.connected && now_ms >= self.next_check_ms {
                self.send_check(h, now_ms);
                self.next_check_ms = now_ms + self.check_ms;
            }
            if self.dirty {
                self.rebuild_overlay(h);
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
            | (st << S_CLOCK_SHIFT)
    }

    fn step_once<H: Host>(&mut self, h: &mut H) {
        h.park_step(SLOT_JOURNAL);
        if !self.dirty {
            h.park_step(SLOT_PRESENTED);
        }
        self.tick = h.park_tick(SLOT_JOURNAL);
        let hash = h.park_hash(SLOT_JOURNAL);
        // The ring entry is the state at *entry* to this tick: the events
        // stamped for it have not been applied yet, and re-hashing after
        // they are would poison exactly the entry a same-tick check
        // samples.
        self.hash_set(self.tick, hash);
        if self.tick % self.hz == 0 {
            let len = h.park_snapshot(SLOT_JOURNAL, &mut bufs().state) as usize;
            if len > 0 && len <= SNAP_CAP {
                self.ring_push(len);
            }
            if !self.replaying {
                self.prune_recent();
            }
        }
    }

    /// Applies queued events at their exact tick, in dense seq order. A
    /// gap waits; a late event rolls back through the snapshot ring and
    /// replays; deeper than the ring is a resync.
    fn apply_ready<H: Host>(&mut self, h: &mut H, now_ms: u64) {
        while self.queued_len > 0 && bufs().queued[0].seq <= self.seq {
            self.queued_drop_front();
        }
        while self.queued_len > 0 && bufs().queued[0].seq == self.seq + 1 {
            let e = bufs().queued[0];
            if e.tick > self.tick {
                return;
            }
            if e.tick < self.tick && !self.rollback_to(h, e.tick) {
                self.request_resync(h, R_LATE_EVENT, now_ms);
                return;
            }
            self.queued_drop_front();
            let code = self.apply(h, SLOT_JOURNAL, e.kind, e.payload());
            if code != 0 {
                self.request_resync(h, R_EVENT_REJECTED, now_ms);
                return;
            }
            self.seq = e.seq;
            self.stats[STAT_EVENTS as usize - 1] += 1;
            self.dirty = true;
            self.recent_push(e);
            if self.intent_take(e.intent).is_some() {
                self.dirty = true;
            }
            h.emit(T_EVENT_APPLIED, e.seq as u64, e.tick);
            if e.kind == EV_EPOCH_ADVANCE && e.plen == 12 {
                let mut b = [0u8; 8];
                b.copy_from_slice(&e.p[4..12]);
                self.want_module(h, u64::from_le_bytes(b) as u32, now_ms);
            }
        }
    }

    /// Rewinds to the newest ring entry at or before `tick`, replays the
    /// events since it, and stops exactly at `tick` so the late event can
    /// apply where it belongs. The deficit that opens up is the clock's
    /// problem, and stepping is what it is for. False means the repair is
    /// out of reach and only a snapshot will do.
    fn rollback_to<H: Host>(&mut self, h: &mut H, tick: u64) -> bool {
        let mut i = self.ring_len;
        while i > 0 {
            i -= 1;
            let idx = self.ring_idx(i);
            if bufs().ring[idx].tick > tick {
                continue;
            }
            let was = self.tick;
            let entry = bufs().ring[idx];
            let (stick, sseq, slen) = (entry.tick, entry.seq, entry.len);
            // The authority stamps ticks monotonically with seq, so every
            // event to replay belongs at or before `tick`. One that does
            // not would be silently dropped by this replay: refuse.
            for k in 0..self.recent_len {
                let e = self.recent_get(k);
                if e.seq > sseq && e.tick > tick {
                    return false;
                }
            }
            if h.park_restore(SLOT_JOURNAL, &bufs().ring[idx].state[..slen]) != 0 {
                return false;
            }
            self.seq = sseq;
            self.tick = h.park_tick(SLOT_JOURNAL);
            self.dirty = true;
            self.stats[STAT_ROLLBACKS as usize - 1] += 1;
            h.emit(T_ROLLBACK, was.saturating_sub(stick), stick);
            self.replaying = true;
            for k in 0..self.recent_len {
                let e = self.recent_get(k);
                if e.seq <= sseq {
                    continue;
                }
                while self.tick < e.tick {
                    self.step_once(h);
                }
                if self.apply(h, SLOT_JOURNAL, e.kind, e.payload()) == 0 {
                    self.seq = e.seq;
                }
            }
            while self.tick < tick {
                self.step_once(h);
            }
            self.replaying = false;
            return true;
        }
        false
    }

    /// Slot 1 is slot 0 plus this client's unanswered intents. Rebuilt
    /// whenever slot 0 moved outside lockstep stepping or the pending set
    /// changed; overlay rejects are expected and ignored.
    fn rebuild_overlay<H: Host>(&mut self, h: &mut H) {
        let len = h.park_snapshot(SLOT_JOURNAL, &mut bufs().state) as usize;
        if len == 0 || len > SNAP_CAP {
            return;
        }
        if h.park_restore(SLOT_PRESENTED, &bufs().state[..len]) != 0 {
            return;
        }
        for i in 0..self.intents_len {
            let it = bufs().intents[i];
            self.apply(h, SLOT_PRESENTED, it.kind, it.payload());
        }
        self.dirty = false;
    }

    fn apply<H: Host>(&mut self, h: &mut H, slot: u32, kind: u16, payload: &[u8]) -> u32 {
        let mut buf = [0u8; 2 + EV_PAYLOAD_CAP];
        buf[..2].copy_from_slice(&kind.to_le_bytes());
        buf[2..2 + payload.len()].copy_from_slice(payload);
        h.park_apply(slot, &buf[..2 + payload.len()])
    }

    // ---- checks, strikes, resync ----

    fn send_check<H: Host>(&mut self, h: &mut H, now_ms: u64) {
        let Some(wh) = self.hash_get(self.tick) else {
            return;
        };
        let n = wire::encode_check(&mut bufs().out, self.tick, wh, now_ms);
        h.send_datagram(&bufs().out[..n]);
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

//! The park module: the complete game state machine, compiled to wasm and
//! run identically by the server authority, every browser, and every load
//! bot (docs/netcode.md). All state transitions live here — the hosts move
//! opaque bytes and never interpret game rules.
//!
//! Replication contract:
//!   - `sim_step()` advances tick T -> T+1 as a pure function of state.
//!   - A journal event stamped tick T applies when `sim_tick() == T`,
//!     before the step out of T; events within a tick apply in seq order.
//!     The authority stamps events with its current tick and follows the
//!     same rule, so every replica derives bit-identical state.
//!   - `sim_apply` validates and applies in one call and MUST NOT mutate
//!     on reject — it doubles as the authority's validation.
//!   - Terrain is not state: it is a content-addressed artifact the host
//!     loads through `sim_set_terrain` before `sim_init`/`sim_restore` and
//!     around every `terrain_set` event. The state holds only the
//!     artifact's identity, which every boundary call cross-checks.
//!   - The reads are inert. `sim_hash`, `sim_view`, `sim_snapshot`, and
//!     `sim_hud` touch no state, so a replica calling them every tick
//!     derives the same world as an authority that barely calls them at
//!     all. Measured rather than assumed: 600 ticks of one instance
//!     polling all four every tick, against another polling none, is
//!     hash-identical tick for tick. A divergence hunt can rule this out
//!     on sight, and so can a rollback that snapshots per tick to record
//!     its repair floors.
//!
//! Movement is stored as exactly (position, waypoint, target) per dog.
//! Paths are never cached: the waypoint is recomputed only at state
//! transitions (new target, corner reached, blocked), so a restored
//! snapshot continues movement bit-identically — there is no derived
//! structure whose absence could diverge a replay.
//!
//! ABI (all integers; payloads move through the io buffer, terrain blobs
//! through the larger terrain buffer):
//!   io_buf() -> *mut u8, io_cap() -> u32
//!   terrain_buf() -> *mut u8, terrain_cap() -> u32
//!   sim_set_terrain(len: u32) -> u32  parse+adopt blob, 0 ok
//!   sim_init(seed: u64, park_id: u64, epoch: u32) -> u32
//!   sim_restore(len: u32) -> u32      0 ok, else restore error code
//!   sim_snapshot() -> u32             canonical snapshot written to io, len
//!   sim_step()
//!   sim_apply(len: u32) -> u32        0 ok, else reject code
//!   sim_hash() -> u64                 canonical state hash
//!   sim_tick() -> u64, sim_epoch() -> u32, sim_terrain_id() -> u64
//!   sim_rate() -> u32, sim_anchor_tick() -> u64, sim_anchor_ns() -> u64
//!   sim_view() -> u32                 render data written to io, len
//!   sim_hud(self_id: u64) -> u32      HUD numbers written to io, len
//!
//! Event encoding (little-endian): kind u16, then payload —
//!   1 join          { id u64 }            spawn cell derived from
//!                                         det_rand over standable ground
//!   2 leave         { id u64 }
//!   3 check_in      { id u64 }            once per day index per dog
//!   4 move_to       { id u64, node u16 }  set the dog's movement target
//!   5 day_reset     { day u32 }           system event; re-arms check-ins
//!   6 epoch_advance { epoch u32, module_hash u64 }  system event
//!   7 terrain_set   { schema u32, terrain_id u64 }  system event; the
//!                                         loaded blob must already match
//!   8 boost_set     { id u64, on u8 }     held-button 2x speed; only
//!                                         transitions journal (a no-op
//!                                         set rejects with ERR_NOOP)
//!   9 clock_skip    { to_tick u64 }       system event; jumps the tick
//!                                         forward without stepping — the
//!                                         journaled repayment of authority
//!                                         downtime. Dogs, energy, and day
//!                                         are untouched; forward only
//!  10 rate_set      { hz u32 }            system event; changes the tick
//!                                         rate and re-anchors the piecewise
//!                                         tick<->wall mapping at this tick.
//!                                         The rate is world state so every
//!                                         pod generation (and rollback)
//!                                         derives the same schedule
//!
//! Snapshot encoding (canonical; dogs strictly sorted by id; park energy
//! is cumulative — departed dogs' contributions persist — so it is state,
//! not a derivable aggregate):
//!   magic "MYP3", epoch u32, park_id u64, seed u64, tick u64, day u32,
//!   n u32, energy u64, terrain_schema u32, terrain_id u64,
//!   rate_hz u32, anchor_tick u64, anchor_ns u64, then n *
//!   { id u64, x i32, y i32, energy u32, checked_in_day u32, target u16,
//!     waypoint u16, flags u8, pad u8 }
//!   The anchor pair defines the current rate segment: the wall instant of
//!   tick T (T >= anchor_tick) is epoch + anchor_ns + (T-anchor_tick)/hz.
//!   Stored "MYP2" snapshots (the pre-rate era) restore with the genesis
//!   rate (24Hz, anchors 0) and hash identically to how their era hashed
//!   them — the rate fields join the hash only once non-default.
//!   x, y are Q16.16 park-cell coordinates; target/waypoint are nav nodes
//!   (0xFFFF = none); flags bit 0 = standing on a deck, bit 1 = boosting.
#![cfg_attr(not(test), no_std)]

use mythra_sim_core::det_rand;
use mythra_sim_nav as nav;
use mythra_sim_terrain::{BLOCK, MAX_BLOB, NONE, Node, SWIM, Terrain};

#[cfg(target_arch = "wasm32")]
#[panic_handler]
fn panic(_: &core::panic::PanicInfo) -> ! {
    core::arch::wasm32::unreachable()
}

pub const MAX_DOGS: usize = 2048;
const IO_CAP: usize = 64 * 1024;
// MAGIC (the MYP2 constant) stays first in the hash stream forever: it is
// domain separation, not data, and swapping it would orphan every stored
// world hash. MAGIC3 versions only the snapshot byte encoding.
const MAGIC: u32 = u32::from_le_bytes(*b"MYP2");
const MAGIC3: u32 = u32::from_le_bytes(*b"MYP3");
const NEVER: u32 = u32::MAX;
const DOG_REC: usize = 30;
const HEADER: usize = 60;
const HEADER3: usize = 80;
pub const GENESIS_HZ: u32 = 24;
pub const MAX_HZ: u32 = 1000;

pub const EV_JOIN: u16 = 1;
pub const EV_LEAVE: u16 = 2;
pub const EV_CHECK_IN: u16 = 3;
pub const EV_MOVE_TO: u16 = 4;
pub const EV_DAY_RESET: u16 = 5;
pub const EV_EPOCH_ADVANCE: u16 = 6;
pub const EV_TERRAIN_SET: u16 = 7;
pub const EV_BOOST_SET: u16 = 8;
pub const EV_CLOCK_SKIP: u16 = 9;
pub const EV_RATE_SET: u16 = 10;

pub const OK: u32 = 0;
pub const ERR_ENCODING: u32 = 1;
pub const ERR_PRESENT: u32 = 2;
pub const ERR_ABSENT: u32 = 3;
pub const ERR_FULL: u32 = 4;
pub const ERR_CHECKED_IN: u32 = 5;
pub const ERR_KIND: u32 = 6;
pub const ERR_EPOCH: u32 = 7;
pub const ERR_TARGET: u32 = 8;
pub const ERR_TERRAIN: u32 = 9;
pub const ERR_NOOP: u32 = 10;
pub const ERR_TICK: u32 = 11;

const CHECK_IN_ENERGY: u32 = 10;
/// A standing dog rolls to wander once per this many ticks on average
/// (about eight seconds), out to this many cells away.
const WANDER_ODDS: u32 = 192;
const WANDER_RANGE: u32 = 8;
const FLAG_DECK: u8 = 1;
const FLAG_BOOST: u8 = 1 << 1;
const FLAGS_VALID: u8 = FLAG_DECK | FLAG_BOOST;

#[derive(Clone, Copy)]
struct Dog {
    id: u64,
    x: i32,
    y: i32,
    energy: u32,
    checked_in_day: u32,
    target: u16,
    waypoint: u16,
    flags: u8,
}

const EMPTY_DOG: Dog = Dog {
    id: 0,
    x: 0,
    y: 0,
    energy: 0,
    checked_in_day: NEVER,
    target: NONE.0,
    waypoint: NONE.0,
    flags: 0,
};

struct Park {
    epoch: u32,
    park_id: u64,
    seed: u64,
    tick: u64,
    day: u32,
    energy: u64,
    terrain_schema: u32,
    terrain_id: u64,
    // The current rate segment of the piecewise tick<->wall mapping: tick
    // anchor_tick sits anchor_ns nanoseconds after the wall epoch, and
    // later ticks advance at rate_hz. State — not host config — so every
    // pod generation and every replay derives the identical schedule.
    rate_hz: u32,
    anchor_tick: u64,
    anchor_ns: u64,
    n: usize,
    dogs: [Dog; MAX_DOGS],
}

static mut PARK: Park = Park {
    epoch: 0,
    park_id: 0,
    seed: 0,
    tick: 0,
    day: 0,
    energy: 0,
    terrain_schema: 0,
    terrain_id: 0,
    rate_hz: GENESIS_HZ,
    anchor_tick: 0,
    anchor_ns: 0,
    n: 0,
    dogs: [EMPTY_DOG; MAX_DOGS],
};

static mut IO: [u8; IO_CAP] = [0; IO_CAP];
static mut TERRAIN_BLOB: [u8; MAX_BLOB] = [0; MAX_BLOB];
static mut TERRAIN: Option<Terrain<'static>> = None;
static mut SCRATCH: nav::Scratch = nav::Scratch::NEW;

fn park() -> &'static mut Park {
    unsafe { &mut *(&raw mut PARK) }
}

fn io() -> &'static mut [u8; IO_CAP] {
    unsafe { &mut *(&raw mut IO) }
}

fn terrain() -> Option<Terrain<'static>> {
    unsafe { *(&raw const TERRAIN) }
}

fn scratch() -> &'static mut nav::Scratch {
    unsafe { &mut *(&raw mut SCRATCH) }
}

#[unsafe(no_mangle)]
pub extern "C" fn io_buf() -> *mut u8 {
    &raw mut IO as *mut u8
}

#[unsafe(no_mangle)]
pub extern "C" fn io_cap() -> u32 {
    IO_CAP as u32
}

#[unsafe(no_mangle)]
pub extern "C" fn terrain_buf() -> *mut u8 {
    &raw mut TERRAIN_BLOB as *mut u8
}

#[unsafe(no_mangle)]
pub extern "C" fn terrain_cap() -> u32 {
    MAX_BLOB as u32
}

/// Parses and adopts the blob the host wrote into the terrain buffer.
/// Writing that buffer invalidates any previously loaded terrain, so on a
/// parse failure no terrain is loaded — the host must succeed here before
/// touching any other entry point.
#[unsafe(no_mangle)]
pub extern "C" fn sim_set_terrain(len: u32) -> u32 {
    unsafe { *(&raw mut TERRAIN) = None };
    let len = len as usize;
    if len > MAX_BLOB {
        return ERR_ENCODING;
    }
    let blob: &'static [u8] =
        unsafe { core::slice::from_raw_parts(&raw const TERRAIN_BLOB as *const u8, len) };
    match Terrain::parse(blob) {
        Ok(t) => {
            unsafe { *(&raw mut TERRAIN) = Some(t) };
            OK
        }
        Err(code) => code,
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn sim_init(seed: u64, park_id: u64, epoch: u32) -> u32 {
    let Some(t) = terrain() else {
        return ERR_TERRAIN;
    };
    let p = park();
    *p = Park {
        epoch,
        park_id,
        seed,
        tick: 0,
        day: 0,
        energy: 0,
        terrain_schema: t.schema,
        terrain_id: t.id,
        rate_hz: GENESIS_HZ,
        anchor_tick: 0,
        anchor_ns: 0,
        n: 0,
        dogs: [EMPTY_DOG; MAX_DOGS],
    };
    OK
}

#[unsafe(no_mangle)]
pub extern "C" fn sim_tick() -> u64 {
    park().tick
}

#[unsafe(no_mangle)]
pub extern "C" fn sim_epoch() -> u32 {
    park().epoch
}

#[unsafe(no_mangle)]
pub extern "C" fn sim_terrain_id() -> u64 {
    park().terrain_id
}

#[unsafe(no_mangle)]
pub extern "C" fn sim_rate() -> u32 {
    park().rate_hz
}

#[unsafe(no_mangle)]
pub extern "C" fn sim_anchor_tick() -> u64 {
    park().anchor_tick
}

#[unsafe(no_mangle)]
pub extern "C" fn sim_anchor_ns() -> u64 {
    park().anchor_ns
}

fn node_of(t: &Terrain, d: &Dog) -> Node {
    Node::at(t.idx(d.x >> 16, d.y >> 16), d.flags & FLAG_DECK != 0)
}

/// One tick: every dog runs its movement state machine, in roster (id)
/// order. Pure function of state; identical on every surface by
/// construction. Terrain identity is re-checked so a host that violated
/// the load contract freezes movement instead of computing on the wrong
/// world (the hash oracle will surface it).
#[unsafe(no_mangle)]
pub extern "C" fn sim_step() {
    let p = park();
    if let Some(t) = terrain() {
        if t.id == p.terrain_id {
            for i in 0..p.n {
                step_dog(p, &t, i);
            }
        }
    }
    p.tick += 1;
}

/// While boosted and otherwise idle, a dog patrols the park's primary
/// bridge: back and forth between the standable ground endpoints of the
/// first deck span (row-major order, horizontal runs). Derived purely
/// from terrain data — no fixture knowledge — and stateless: the leg
/// target is the reachable endpoint with the greater PATH cost under the
/// same deterministic A* movement runs on, so after each arrival the
/// other end is chosen and the ping-pong emerges from (position,
/// terrain) alone. Unreachable ends are never armed, so a stranded dog
/// costs at most its component's worth of failed search per tick while
/// the button is held. Gives held-boost a continuous, predictable
/// trajectory for cross-client latency checks.
fn patrol_target(t: &Terrain, s: &mut nav::Scratch, from: Node) -> Node {
    let mut first = None;
    for i in 0..t.cells() as u16 {
        if t.deck_elev(i).is_some() {
            first = Some(i);
            break;
        }
    }
    let Some(first) = first else {
        return NONE;
    };
    let (x0, row) = t.xy(first);
    let mut x1 = x0;
    while t.in_bounds(x1 + 1, row) && t.deck_elev(t.idx(x1 + 1, row)).is_some() {
        x1 += 1;
    }
    let end = |ex: i32| -> Node {
        if t.in_bounds(ex, row) {
            t.nearest_ground(t.idx(ex, row))
        } else {
            NONE
        }
    };
    let mut best = NONE;
    let mut best_cost = 0u32;
    for cand in [end(x0 - 1), end(x1 + 1)] {
        if cand == NONE || cand.idx() == from.idx() {
            continue;
        }
        if let Some(cost) = nav::path_cost(t, s, from, cand) {
            // strict > keeps the tie-break on the west end (visited first)
            if best == NONE || cost > best_cost {
                best = cand;
                best_cost = cost;
            }
        }
    }
    best
}

fn step_dog(p: &mut Park, t: &Terrain, i: usize) {
    let (seed, tick) = (p.seed, p.tick);
    let d = &mut p.dogs[i];
    if d.target == NONE.0 {
        // a held boost turns idleness into the bridge patrol; an explicit
        // move order (target already set) always wins, and releasing the
        // button just stops re-arming the next leg
        if d.flags & FLAG_BOOST != 0 {
            let here = node_of(t, d);
            let pt = patrol_target(t, scratch(), here);
            if pt != NONE {
                d.target = pt.0;
                d.waypoint = NONE.0;
                return;
            }
        }
        // idle: roll to wander somewhere nearby and standable
        if det_rand(seed, tick, d.id, WANDER_ODDS) != 0 {
            return;
        }
        let span = 2 * WANDER_RANGE + 1;
        let dx = det_rand(seed, tick, d.id ^ 0x5741_4E44, span) as i32 - WANDER_RANGE as i32;
        let dy = det_rand(seed, tick, d.id ^ 0x5741_4E45, span) as i32 - WANDER_RANGE as i32;
        let (cx, cy) = ((d.x >> 16) + dx, (d.y >> 16) + dy);
        if (dx == 0 && dy == 0) || !t.in_bounds(cx, cy) {
            return;
        }
        let idx = t.idx(cx, cy);
        if t.class(idx) == BLOCK || idx == t.idx(d.x >> 16, d.y >> 16) {
            return;
        }
        d.target = Node::ground(idx).0;
        d.waypoint = NONE.0;
        return;
    }
    if d.waypoint == NONE.0 {
        let here = node_of(t, d);
        if here.0 == d.target {
            d.target = NONE.0;
            return;
        }
        let wp = nav::first_waypoint(t, scratch(), here, Node(d.target));
        if wp == NONE {
            d.target = NONE.0;
            return;
        }
        d.waypoint = wp.0;
    }
    let mut on_deck = d.flags & FLAG_DECK != 0;
    let boosted = d.flags & FLAG_BOOST != 0;
    let outcome = nav::step_toward(
        t,
        &mut d.x,
        &mut d.y,
        &mut on_deck,
        Node(d.waypoint),
        boosted,
    );
    d.flags = if on_deck {
        d.flags | FLAG_DECK
    } else {
        d.flags & !FLAG_DECK
    };
    match outcome {
        nav::Step::Arrived => {
            if d.waypoint == d.target {
                d.target = NONE.0;
            }
            d.waypoint = NONE.0;
        }
        nav::Step::Blocked => {
            // another dog's world can't block (no dog-dog collision yet);
            // a blocked step means the straight run is obstructed, so give
            // up rather than burn a search every tick against a wall
            d.target = NONE.0;
            d.waypoint = NONE.0;
        }
        nav::Step::Moving => {}
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn sim_apply(len: u32) -> u32 {
    let len = len as usize;
    if len > IO_CAP || len < 2 {
        return ERR_ENCODING;
    }
    let (kind, body) = {
        let buf = &io()[..len];
        (u16::from_le_bytes([buf[0], buf[1]]), 2usize)
    };
    match kind {
        EV_JOIN => {
            let Some(id) = read_u64_exact(body, len) else {
                return ERR_ENCODING;
            };
            let Some(t) = terrain() else {
                return ERR_TERRAIN;
            };
            let p = park();
            if find(p, id).is_some() {
                return ERR_PRESENT;
            }
            if p.n >= MAX_DOGS {
                return ERR_FULL;
            }
            let spawn = spawn_cell(&t, p.seed, p.tick, id);
            let (x, y) = nav::center(&t, spawn);
            insert(
                p,
                Dog {
                    id,
                    x,
                    y,
                    energy: 0,
                    checked_in_day: NEVER,
                    target: NONE.0,
                    waypoint: NONE.0,
                    flags: 0,
                },
            );
            OK
        }
        EV_LEAVE => {
            let Some(id) = read_u64_exact(body, len) else {
                return ERR_ENCODING;
            };
            let p = park();
            let Some(i) = find(p, id) else {
                return ERR_ABSENT;
            };
            p.dogs.copy_within(i + 1..p.n, i);
            p.n -= 1;
            p.dogs[p.n] = EMPTY_DOG;
            OK
        }
        EV_CHECK_IN => {
            let Some(id) = read_u64_exact(body, len) else {
                return ERR_ENCODING;
            };
            let p = park();
            let day = p.day;
            let Some(i) = find(p, id) else {
                return ERR_ABSENT;
            };
            if p.dogs[i].checked_in_day == day {
                return ERR_CHECKED_IN;
            }
            p.dogs[i].checked_in_day = day;
            p.dogs[i].energy += CHECK_IN_ENERGY;
            p.energy += CHECK_IN_ENERGY as u64;
            OK
        }
        EV_MOVE_TO => {
            if len != body + 10 {
                return ERR_ENCODING;
            }
            let buf = &io()[..len];
            let mut b8 = [0u8; 8];
            b8.copy_from_slice(&buf[body..body + 8]);
            let id = u64::from_le_bytes(b8);
            let node = Node(u16::from_le_bytes([buf[body + 8], buf[body + 9]]));
            let Some(t) = terrain() else {
                return ERR_TERRAIN;
            };
            if !t.exists(node) {
                return ERR_TARGET;
            }
            let p = park();
            let Some(i) = find(p, id) else {
                return ERR_ABSENT;
            };
            p.dogs[i].target = node.0;
            p.dogs[i].waypoint = NONE.0;
            OK
        }
        EV_BOOST_SET => {
            if len != body + 9 {
                return ERR_ENCODING;
            }
            let (id, on) = {
                let buf = &io()[..len];
                let mut b8 = [0u8; 8];
                b8.copy_from_slice(&buf[body..body + 8]);
                (u64::from_le_bytes(b8), buf[body + 8])
            };
            if on > 1 {
                return ERR_ENCODING;
            }
            let p = park();
            let Some(i) = find(p, id) else {
                return ERR_ABSENT;
            };
            let cur = p.dogs[i].flags & FLAG_BOOST != 0;
            if cur == (on == 1) {
                // Only transitions journal: a resend or a held-button
                // heartbeat must not mint events.
                return ERR_NOOP;
            }
            p.dogs[i].flags ^= FLAG_BOOST;
            OK
        }
        EV_DAY_RESET => {
            let Some(day) = read_u32_exact(body, len) else {
                return ERR_ENCODING;
            };
            park().day = day;
            OK
        }
        EV_CLOCK_SKIP => {
            let Some(to) = read_u64_exact(body, len) else {
                return ERR_ENCODING;
            };
            let p = park();
            if to <= p.tick {
                return ERR_TICK;
            }
            p.tick = to;
            OK
        }
        EV_RATE_SET => {
            let Some(hz) = read_u32_exact(body, len) else {
                return ERR_ENCODING;
            };
            if hz == 0 || hz > MAX_HZ {
                return ERR_ENCODING;
            }
            let p = park();
            if hz == p.rate_hz {
                return ERR_NOOP;
            }
            // Close the old rate segment: the elapsed wall time of its
            // ticks folds into the anchor, so the mapping stays piecewise
            // exact. u128 keeps tick*1e9 from overflowing; integer ns
            // division is the canonical rounding every replica shares.
            let elapsed = (p.tick - p.anchor_tick) as u128;
            p.anchor_ns += (elapsed * 1_000_000_000u128 / p.rate_hz as u128) as u64;
            p.anchor_tick = p.tick;
            p.rate_hz = hz;
            OK
        }
        EV_EPOCH_ADVANCE => {
            if len != body + 12 {
                return ERR_ENCODING;
            }
            let epoch = {
                let buf = &io()[..len];
                u32::from_le_bytes([buf[body], buf[body + 1], buf[body + 2], buf[body + 3]])
            };
            let p = park();
            if epoch <= p.epoch {
                return ERR_EPOCH;
            }
            p.epoch = epoch;
            OK
        }
        EV_TERRAIN_SET => {
            if len != body + 12 {
                return ERR_ENCODING;
            }
            let (schema, tid) = {
                let buf = &io()[..len];
                let mut b8 = [0u8; 8];
                b8.copy_from_slice(&buf[body + 4..body + 12]);
                (
                    u32::from_le_bytes([buf[body], buf[body + 1], buf[body + 2], buf[body + 3]]),
                    u64::from_le_bytes(b8),
                )
            };
            // The host fetches and loads the blob before feeding the
            // event (the same choreography as module epochs); adopting an
            // identity the loaded blob doesn't carry would fork the world.
            let Some(t) = terrain() else {
                return ERR_TERRAIN;
            };
            if t.schema != schema || t.id != tid {
                return ERR_TERRAIN;
            }
            let p = park();
            p.terrain_schema = schema;
            p.terrain_id = tid;
            for i in 0..p.n {
                let d = &mut p.dogs[i];
                // movement intent does not survive a world change
                d.target = NONE.0;
                d.waypoint = NONE.0;
                let (cx, cy) = (
                    (d.x >> 16).clamp(0, t.w as i32 - 1),
                    (d.y >> 16).clamp(0, t.h as i32 - 1),
                );
                let idx = t.idx(cx, cy);
                let node = Node::at(idx, d.flags & FLAG_DECK != 0);
                if (cx, cy) != (d.x >> 16, d.y >> 16) || !t.exists(node) {
                    let safe = t.nearest_ground(idx);
                    let (x, y) = nav::center(&t, safe);
                    d.x = x;
                    d.y = y;
                    d.flags &= !FLAG_DECK;
                }
            }
            OK
        }
        _ => ERR_KIND,
    }
}

/// Deterministic spawn: a bounded number of seeded draws over the whole
/// park, falling back to the nearest standable ground to the center. The
/// fallback is total for any park with at least one standable cell.
fn spawn_cell(t: &Terrain, seed: u64, tick: u64, id: u64) -> Node {
    let cells = t.cells() as u32;
    for k in 0..32u64 {
        let idx = det_rand(
            seed,
            tick,
            id.wrapping_add(k.wrapping_mul(0x9E37_79B9_7F4A_7C15)),
            cells,
        ) as u16;
        let (x, y) = nav::center(t, Node::ground(idx));
        if t.class(idx) != BLOCK && !t.subtile_blocked(x, y) {
            return Node::ground(idx);
        }
    }
    t.nearest_ground(t.idx(t.w as i32 / 2, t.h as i32 / 2))
}

fn read_u64_exact(at: usize, len: usize) -> Option<u64> {
    if len != at + 8 {
        return None;
    }
    let buf = &io()[..len];
    let mut b = [0u8; 8];
    b.copy_from_slice(&buf[at..at + 8]);
    Some(u64::from_le_bytes(b))
}

fn read_u32_exact(at: usize, len: usize) -> Option<u32> {
    if len != at + 4 {
        return None;
    }
    let buf = &io()[..len];
    let mut b = [0u8; 4];
    b.copy_from_slice(&buf[at..at + 4]);
    Some(u32::from_le_bytes(b))
}

fn find(p: &Park, id: u64) -> Option<usize> {
    p.dogs[..p.n].binary_search_by_key(&id, |d| d.id).ok()
}

fn insert(p: &mut Park, d: Dog) {
    let i = match p.dogs[..p.n].binary_search_by_key(&d.id, |q| q.id) {
        Ok(i) | Err(i) => i,
    };
    p.dogs.copy_within(i..p.n, i + 1);
    p.dogs[i] = d;
    p.n += 1;
}

#[unsafe(no_mangle)]
pub extern "C" fn sim_snapshot() -> u32 {
    let p = park();
    let n = p.n;
    let total = HEADER3 + n * DOG_REC;
    let buf = io();
    buf[0..4].copy_from_slice(&MAGIC3.to_le_bytes());
    buf[4..8].copy_from_slice(&p.epoch.to_le_bytes());
    buf[8..16].copy_from_slice(&p.park_id.to_le_bytes());
    buf[16..24].copy_from_slice(&p.seed.to_le_bytes());
    buf[24..32].copy_from_slice(&p.tick.to_le_bytes());
    buf[32..36].copy_from_slice(&p.day.to_le_bytes());
    buf[36..40].copy_from_slice(&(n as u32).to_le_bytes());
    buf[40..48].copy_from_slice(&p.energy.to_le_bytes());
    buf[48..52].copy_from_slice(&p.terrain_schema.to_le_bytes());
    buf[52..60].copy_from_slice(&p.terrain_id.to_le_bytes());
    buf[60..64].copy_from_slice(&p.rate_hz.to_le_bytes());
    buf[64..72].copy_from_slice(&p.anchor_tick.to_le_bytes());
    buf[72..80].copy_from_slice(&p.anchor_ns.to_le_bytes());
    let mut at = HEADER3;
    for d in &p.dogs[..n] {
        buf[at..at + 8].copy_from_slice(&d.id.to_le_bytes());
        buf[at + 8..at + 12].copy_from_slice(&d.x.to_le_bytes());
        buf[at + 12..at + 16].copy_from_slice(&d.y.to_le_bytes());
        buf[at + 16..at + 20].copy_from_slice(&d.energy.to_le_bytes());
        buf[at + 20..at + 24].copy_from_slice(&d.checked_in_day.to_le_bytes());
        buf[at + 24..at + 26].copy_from_slice(&d.target.to_le_bytes());
        buf[at + 26..at + 28].copy_from_slice(&d.waypoint.to_le_bytes());
        buf[at + 28] = d.flags;
        buf[at + 29] = 0;
        at += DOG_REC;
    }
    total as u32
}

#[unsafe(no_mangle)]
pub extern "C" fn sim_restore(len: u32) -> u32 {
    let len = len as usize;
    if len < HEADER || len > IO_CAP {
        return 1;
    }
    let Some(t) = terrain() else {
        return 4;
    };
    let buf = io();
    // MYP2 is the stored pre-rate era: same layout, no rate segment, and
    // its worlds ran at the genesis rate with the genesis anchor.
    let header = match u32::from_le_bytes([buf[0], buf[1], buf[2], buf[3]]) {
        m if m == MAGIC3 => HEADER3,
        m if m == MAGIC => HEADER,
        _ => return 1,
    };
    let n = u32::from_le_bytes([buf[36], buf[37], buf[38], buf[39]]) as usize;
    if n > MAX_DOGS || len != header + n * DOG_REC {
        return 2;
    }
    let (rate_hz, anchor_tick, anchor_ns) = if header == HEADER3 {
        let mut b8 = [0u8; 8];
        let hz = u32::from_le_bytes([buf[60], buf[61], buf[62], buf[63]]);
        if hz == 0 || hz > MAX_HZ {
            return 2;
        }
        b8.copy_from_slice(&buf[64..72]);
        let at = u64::from_le_bytes(b8);
        b8.copy_from_slice(&buf[72..80]);
        (hz, at, u64::from_le_bytes(b8))
    } else {
        (GENESIS_HZ, 0, 0)
    };
    let schema = u32::from_le_bytes([buf[48], buf[49], buf[50], buf[51]]);
    let mut b8 = [0u8; 8];
    b8.copy_from_slice(&buf[52..60]);
    let tid = u64::from_le_bytes(b8);
    // Restoring against the wrong world would replay a different park;
    // fail fast so the host's terrain-load contract cannot rot silently.
    if t.schema != schema || t.id != tid {
        return 4;
    }
    let mut dogs = [EMPTY_DOG; MAX_DOGS];
    let mut prev: Option<u64> = None;
    let mut at = header;
    for slot in dogs.iter_mut().take(n) {
        let mut b8 = [0u8; 8];
        b8.copy_from_slice(&buf[at..at + 8]);
        let id = u64::from_le_bytes(b8);
        if let Some(prev_id) = prev {
            if id <= prev_id {
                return 3;
            }
        }
        prev = Some(id);
        let rd = |o: usize| {
            let mut b = [0u8; 4];
            b.copy_from_slice(&buf[at + o..at + o + 4]);
            b
        };
        let d = Dog {
            id,
            x: i32::from_le_bytes(rd(8)),
            y: i32::from_le_bytes(rd(12)),
            energy: u32::from_le_bytes(rd(16)),
            checked_in_day: u32::from_le_bytes(rd(20)),
            target: u16::from_le_bytes([buf[at + 24], buf[at + 25]]),
            waypoint: u16::from_le_bytes([buf[at + 26], buf[at + 27]]),
            flags: buf[at + 28],
        };
        let node = Node::at(
            t.idx(
                (d.x >> 16).clamp(0, t.w as i32 - 1),
                (d.y >> 16).clamp(0, t.h as i32 - 1),
            ),
            d.flags & FLAG_DECK != 0,
        );
        if !t.in_bounds(d.x >> 16, d.y >> 16)
            || d.flags & !FLAGS_VALID != 0
            || !t.exists(node)
            || (d.target != NONE.0 && !t.exists(Node(d.target)))
            || (d.waypoint != NONE.0 && !t.exists(Node(d.waypoint)))
        {
            return 5;
        }
        *slot = d;
        at += DOG_REC;
    }
    let p = park();
    p.epoch = u32::from_le_bytes([buf[4], buf[5], buf[6], buf[7]]);
    b8.copy_from_slice(&buf[8..16]);
    p.park_id = u64::from_le_bytes(b8);
    b8.copy_from_slice(&buf[16..24]);
    p.seed = u64::from_le_bytes(b8);
    b8.copy_from_slice(&buf[24..32]);
    p.tick = u64::from_le_bytes(b8);
    p.day = u32::from_le_bytes([buf[32], buf[33], buf[34], buf[35]]);
    b8.copy_from_slice(&buf[40..48]);
    p.energy = u64::from_le_bytes(b8);
    p.terrain_schema = schema;
    p.terrain_id = tid;
    p.rate_hz = rate_hz;
    p.anchor_tick = anchor_tick;
    p.anchor_ns = anchor_ns;
    p.n = n;
    p.dogs = dogs;
    0
}

/// Canonical state hash: FNV-1a over every state field in snapshot order,
/// finalized with a splitmix64 mix. Two replicas holding the same state
/// produce the same value; any single divergence changes it.
///
/// Sixty-four bits of identity, not a number. It is declared i64 because
/// that is what wasm offers, so a host reading it as signed while the wire
/// carries it unsigned will find half of all hashes disagreeing with
/// themselves: `uint32`-truncate i32 results in Go, `BigInt.asUintN(64,
/// ..)` in JavaScript, and normalize before comparing rather than only
/// before printing — a formatter that normalizes while the comparison does
/// not will show two identical hex strings and call them unequal.
///
/// The failure shapes are worth telling apart. A real divergence is
/// systematic: the worlds part company at some tick and stay parted.
/// Agreement that alternates tick by tick, with no relation to anything in
/// the world, is a comparison bug — no state difference and no timing
/// difference can produce it, because the tick is inside the hash and two
/// replicas at different ticks disagree about every check, not some.
#[unsafe(no_mangle)]
pub extern "C" fn sim_hash() -> u64 {
    let p = park();
    let mut h = Fnv::new();
    h.u32(MAGIC);
    h.u32(p.epoch);
    h.u64(p.park_id);
    h.u64(p.seed);
    h.u64(p.tick);
    h.u32(p.day);
    h.u32(p.n as u32);
    h.u64(p.energy);
    h.u32(p.terrain_schema);
    h.u64(p.terrain_id);
    // The rate segment joins the hash stream only once it leaves the
    // genesis value: every world hash minted before rates existed remains
    // valid, so stored snapshots verify across the format bump.
    if (p.rate_hz, p.anchor_tick, p.anchor_ns) != (GENESIS_HZ, 0, 0) {
        h.u32(p.rate_hz);
        h.u64(p.anchor_tick);
        h.u64(p.anchor_ns);
    }
    for d in &p.dogs[..p.n] {
        h.u64(d.id);
        h.u32(d.x as u32);
        h.u32(d.y as u32);
        h.u32(d.energy);
        h.u32(d.checked_in_day);
        h.u32(d.target as u32);
        h.u32(d.waypoint as u32);
        h.u32(d.flags as u32);
    }
    mythra_sim_core::mix64(h.0)
}

/// Presentation projection, never hashed: one 20-byte record per dog —
/// id u64, x i32, y i32 (Q16.16 cells), flags u8 (bit0 deck, bit1
/// swimming, bit2 moving, bit3 boosting), facing u8 (octant, 0=E
/// clockwise), anim u8, pad u8. Facing and animation are derived here so
/// every renderer shows the same dog doing the same thing.
#[unsafe(no_mangle)]
pub extern "C" fn sim_view() -> u32 {
    let p = park();
    let t = terrain();
    let n = p.n;
    let buf = io();
    buf[0..4].copy_from_slice(&(n as u32).to_le_bytes());
    let mut at = 4;
    for d in &p.dogs[..n] {
        let moving = d.target != NONE.0;
        let on_deck = d.flags & FLAG_DECK != 0;
        let mut flags = 0u8;
        let mut facing = 2u8; // idle default: south
        if on_deck {
            flags |= 1;
        }
        if let Some(t) = &t {
            if !on_deck
                && t.in_bounds(d.x >> 16, d.y >> 16)
                && t.class(t.idx(d.x >> 16, d.y >> 16)) == SWIM
            {
                flags |= 2;
            }
            if moving && d.waypoint != NONE.0 {
                let (wx, wy) = nav::center(t, Node(d.waypoint));
                facing = octant(wx - d.x, wy - d.y);
            }
        }
        if moving {
            flags |= 4;
        }
        if d.flags & FLAG_BOOST != 0 {
            flags |= 8;
        }
        let anim = if moving { ((p.tick >> 2) & 3) as u8 } else { 0 };
        buf[at..at + 8].copy_from_slice(&d.id.to_le_bytes());
        buf[at + 8..at + 12].copy_from_slice(&d.x.to_le_bytes());
        buf[at + 12..at + 16].copy_from_slice(&d.y.to_le_bytes());
        buf[at + 16] = flags;
        buf[at + 17] = facing;
        buf[at + 18] = anim;
        buf[at + 19] = 0;
        at += 20;
    }
    at as u32
}

/// The HUD projection, never hashed: the handful of numbers a face plate
/// shows, resolved for one dog so no host has to walk the snapshot to find
/// them. 28 bytes to io — version u16 (=1), present u8, checked_in_today
/// u8, day u32, dog_count u32, park_energy u64, self_energy u32, self_flags
/// u8 (bit 1 = boosting), pad [3]u8.
#[unsafe(no_mangle)]
pub extern "C" fn sim_hud(self_id: u64) -> u32 {
    let p = park();
    let me = find(p, self_id).map(|i| p.dogs[i]);
    let buf = io();
    buf[0..2].copy_from_slice(&1u16.to_le_bytes());
    buf[2] = me.is_some() as u8;
    buf[3] = me.is_some_and(|d| d.checked_in_day == p.day) as u8;
    buf[4..8].copy_from_slice(&p.day.to_le_bytes());
    buf[8..12].copy_from_slice(&(p.n as u32).to_le_bytes());
    buf[12..20].copy_from_slice(&p.energy.to_le_bytes());
    buf[20..24].copy_from_slice(&me.map_or(0, |d| d.energy).to_le_bytes());
    buf[24] = me.map_or(0, |d| d.flags & FLAG_BOOST);
    buf[25..28].copy_from_slice(&[0, 0, 0]);
    28
}

/// Octant of a direction vector: 0=E, 1=SE, 2=S, ... counterclockwise in
/// screen coordinates (y grows south). Dominant-axis with a 2:1 threshold.
fn octant(dx: i32, dy: i32) -> u8 {
    let (ax, ay) = (dx.unsigned_abs() as i64, dy.unsigned_abs() as i64);
    let (ex, ey) = if ax > 2 * ay {
        (dx.signum(), 0)
    } else if ay > 2 * ax {
        (0, dy.signum())
    } else {
        (dx.signum(), dy.signum())
    };
    match (ex, ey) {
        (1, 0) => 0,
        (1, 1) => 1,
        (0, 1) => 2,
        (-1, 1) => 3,
        (-1, 0) => 4,
        (-1, -1) => 5,
        (0, -1) => 6,
        (1, -1) => 7,
        _ => 2,
    }
}

struct Fnv(u64);

impl Fnv {
    fn new() -> Self {
        Fnv(0xCBF2_9CE4_8422_2325)
    }
    fn bytes(&mut self, b: &[u8]) {
        for &x in b {
            self.0 ^= x as u64;
            self.0 = self.0.wrapping_mul(0x0000_0100_0000_01B3);
        }
    }
    fn u32(&mut self, v: u32) {
        self.bytes(&v.to_le_bytes());
    }
    fn u64(&mut self, v: u64) {
        self.bytes(&v.to_le_bytes());
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use mythra_sim_terrain::{BYTES_PER_CELL, Builder, HEADER as T_HEADER, STAIRS, WALK, blob_id};
    use std::sync::{Mutex, MutexGuard};

    // The module state is a single static (as it is in wasm); tests
    // serialize on this lock and re-init in every case.
    static LOCK: Mutex<()> = Mutex::new(());

    // A 12x12 test park: open grass, a river column with a bridge (deck at
    // elev 1 with stairs+platform approaches), and a walled corner.
    fn park_blob() -> Vec<u8> {
        let mut buf = vec![0u8; T_HEADER + 144 * BYTES_PER_CELL];
        let mut b = Builder::new(&mut buf, 12, 12);
        for y in 0..12 {
            b.set_ground(6, y, SWIM, 0);
        }
        for x in [4, 5, 7, 8] {
            b.set_ground(x, 5, WALK, 1);
        }
        b.set_ground(3, 5, STAIRS, 0);
        b.set_ground(9, 5, STAIRS, 0);
        for x in 5..=7 {
            b.set_deck(x, 5, 1);
        }
        b.set_ground(0, 11, BLOCK, 0);
        b.set_ground(1, 11, BLOCK, 0);
        b.set_obstacle(2, 2, 0b0000_0110_0110_0000);
        b.finish().to_vec()
    }

    fn load_terrain(blob: &[u8]) {
        let buf = unsafe { &mut *(&raw mut TERRAIN_BLOB) };
        buf[..blob.len()].copy_from_slice(blob);
        assert_eq!(sim_set_terrain(blob.len() as u32), OK);
    }

    fn setup(seed: u64) -> MutexGuard<'static, ()> {
        let g = LOCK.lock().unwrap_or_else(|e| e.into_inner());
        load_terrain(&park_blob());
        assert_eq!(sim_init(seed, 42, 1), OK);
        g
    }

    fn ev(kind: u16, payload: &[u8]) -> u32 {
        let buf = io();
        buf[0..2].copy_from_slice(&kind.to_le_bytes());
        buf[2..2 + payload.len()].copy_from_slice(payload);
        sim_apply((2 + payload.len()) as u32)
    }

    fn ev_id(kind: u16, id: u64) -> u32 {
        ev(kind, &id.to_le_bytes())
    }

    fn ev_move(id: u64, node: u16) -> u32 {
        let mut p = id.to_le_bytes().to_vec();
        p.extend_from_slice(&node.to_le_bytes());
        ev(EV_MOVE_TO, &p)
    }

    fn ev_boost(id: u64, on: u8) -> u32 {
        let mut p = id.to_le_bytes().to_vec();
        p.push(on);
        ev(EV_BOOST_SET, &p)
    }

    fn snapshot_vec() -> Vec<u8> {
        let len = sim_snapshot() as usize;
        io()[..len].to_vec()
    }

    fn restore_vec(s: &[u8]) {
        io()[..s.len()].copy_from_slice(s);
        assert_eq!(sim_restore(s.len() as u32), 0);
    }

    fn dog(id: u64) -> Dog {
        let p = park();
        p.dogs[find(p, id).expect("dog present")]
    }

    // A scripted journal: (tick, kind, a, b). Applies each event at its
    // tick per the replication contract, stepping between.
    fn run_journal(journal: &[(u64, u16, u64, u16)], until_tick: u64) {
        for &(tick, kind, a, b) in journal {
            while sim_tick() < tick {
                sim_step();
            }
            let code = match kind {
                EV_DAY_RESET => ev(EV_DAY_RESET, &(a as u32).to_le_bytes()),
                EV_CLOCK_SKIP => ev(EV_CLOCK_SKIP, &a.to_le_bytes()),
                EV_RATE_SET => ev(EV_RATE_SET, &(a as u32).to_le_bytes()),
                EV_EPOCH_ADVANCE => {
                    let mut p = (a as u32).to_le_bytes().to_vec();
                    p.extend_from_slice(&0u64.to_le_bytes());
                    ev(EV_EPOCH_ADVANCE, &p)
                }
                EV_MOVE_TO => ev_move(a, b),
                EV_BOOST_SET => ev_boost(a, b as u8),
                _ => ev_id(kind, a),
            };
            assert_eq!(code, OK, "event {kind} at tick {tick}");
        }
        while sim_tick() < until_tick {
            sim_step();
        }
    }

    fn journal() -> Vec<(u64, u16, u64, u16)> {
        let blob = park_blob();
        let t = Terrain::parse(&blob).unwrap();
        let far = Node::ground(t.idx(10, 2)).0; // east side: crosses the river
        let deck = Node::deck(t.idx(6, 5)).0; // mid-bridge
        vec![
            (0, EV_JOIN, 7, 0),
            (0, EV_JOIN, 3, 0),
            (5, EV_JOIN, 11, 0),
            (5, EV_CHECK_IN, 7, 0),
            (8, EV_MOVE_TO, 7, far),
            // a held boost spanning movement, a corner, and its release
            (10, EV_BOOST_SET, 7, 1),
            (30, EV_CHECK_IN, 3, 0),
            (40, EV_MOVE_TO, 3, deck),
            (45, EV_BOOST_SET, 3, 1),
            (70, EV_BOOST_SET, 3, 0),
            (100, EV_LEAVE, 7, 0),
            (240, EV_DAY_RESET, 1, 0),
            (241, EV_CHECK_IN, 3, 0),
            (300, EV_EPOCH_ADVANCE, 2, 0),
        ]
    }

    #[test]
    fn replay_is_deterministic() {
        let _g = setup(99);
        let j = journal();
        run_journal(&j, 900);
        let h1 = sim_hash();
        let s1 = snapshot_vec();
        assert_eq!(sim_init(99, 42, 1), OK);
        run_journal(&j, 900);
        assert_eq!(sim_hash(), h1);
        assert_eq!(snapshot_vec(), s1);
    }

    #[test]
    fn clock_skip_jumps_forward_only() {
        let _g = setup(5);
        assert_eq!(ev_id(EV_JOIN, 3), OK);
        for _ in 0..10 {
            sim_step();
        }
        assert_eq!(ev(EV_CLOCK_SKIP, &10u64.to_le_bytes()), ERR_TICK);
        assert_eq!(ev(EV_CLOCK_SKIP, &3u64.to_le_bytes()), ERR_TICK);
        assert_eq!(ev(EV_CLOCK_SKIP, &10u32.to_le_bytes()), ERR_ENCODING);
        let d_before = dog(3);
        let day_before = park().day;
        assert_eq!(ev(EV_CLOCK_SKIP, &1_000_000u64.to_le_bytes()), OK);
        assert_eq!(sim_tick(), 1_000_000);
        // The jump moves time and nothing else.
        let d = dog(3);
        let before = (d_before.x, d_before.y, d_before.energy);
        assert_eq!((d.x, d.y, d.energy), before);
        assert_eq!(park().day, day_before);
    }

    #[test]
    fn replay_across_clock_skip_is_deterministic() {
        let _g = setup(31);
        let mut j = journal();
        j.push((320, EV_CLOCK_SKIP, 100_000, 0));
        j.push((100_005, EV_JOIN, 21, 0));
        run_journal(&j, 100_300);
        let h1 = sim_hash();
        let s1 = snapshot_vec();
        assert_eq!(sim_init(31, 42, 1), OK);
        run_journal(&j, 100_300);
        assert_eq!(sim_hash(), h1);
        assert_eq!(snapshot_vec(), s1);
    }

    #[test]
    fn rate_set_reanchors_the_piecewise_mapping() {
        let _g = setup(3);
        for _ in 0..48 {
            sim_step(); // 2s at the genesis 24Hz
        }
        assert_eq!(ev(EV_RATE_SET, &0u32.to_le_bytes()), ERR_ENCODING);
        assert_eq!(ev(EV_RATE_SET, &2000u32.to_le_bytes()), ERR_ENCODING);
        assert_eq!(ev(EV_RATE_SET, &24u32.to_le_bytes()), ERR_NOOP);
        assert_eq!(ev(EV_RATE_SET, &120u32.to_le_bytes()), OK);
        assert_eq!(sim_rate(), 120);
        assert_eq!(sim_anchor_tick(), 48);
        assert_eq!(sim_anchor_ns(), 2_000_000_000);
        for _ in 0..120 {
            sim_step(); // 1s at the new rate
        }
        assert_eq!(ev(EV_RATE_SET, &24u32.to_le_bytes()), OK);
        assert_eq!(sim_anchor_tick(), 168);
        assert_eq!(sim_anchor_ns(), 3_000_000_000);
    }

    #[test]
    fn replay_across_rate_set_is_deterministic() {
        let _g = setup(47);
        let mut j = journal();
        j.push((320, EV_RATE_SET, 120, 0));
        j.push((400, EV_JOIN, 21, 0));
        run_journal(&j, 700);
        let h1 = sim_hash();
        let s1 = snapshot_vec();
        assert_eq!(sim_init(47, 42, 1), OK);
        run_journal(&j, 700);
        assert_eq!(sim_hash(), h1);
        assert_eq!(snapshot_vec(), s1);
        // and the snapshot round-trips with the rate segment intact
        restore_vec(&s1);
        assert_eq!(sim_rate(), 120);
        assert_eq!(sim_anchor_tick(), 320);
        assert_eq!(sim_hash(), h1);
    }

    #[test]
    fn myp2_snapshots_restore_with_genesis_rate_and_identical_hash() {
        let _g = setup(9);
        let j = journal();
        run_journal(&j, 500); // never touches the rate: hash omits it
        let h = sim_hash();
        let s3 = snapshot_vec();
        // Rewrite the MYP3 bytes as their MYP2 era form: old magic, no
        // rate segment — exactly what pre-rate rows in park_snapshots hold.
        let mut s2 = Vec::with_capacity(s3.len() - 20);
        s2.extend_from_slice(&s3[..HEADER]);
        s2[0..4].copy_from_slice(&MAGIC.to_le_bytes());
        s2.extend_from_slice(&s3[HEADER3..]);
        restore_vec(&s2);
        assert_eq!(sim_rate(), GENESIS_HZ);
        assert_eq!((sim_anchor_tick(), sim_anchor_ns()), (0, 0));
        assert_eq!(sim_hash(), h, "pre-rate hash must survive the format bump");
        assert_eq!(snapshot_vec(), s3);
    }

    #[test]
    fn snapshot_after_clock_skip_resumes_identically() {
        let _g = setup(13);
        assert_eq!(ev_id(EV_JOIN, 7), OK);
        assert_eq!(ev_id(EV_JOIN, 3), OK);
        for _ in 0..50 {
            sim_step();
        }
        assert_eq!(ev(EV_CLOCK_SKIP, &1_000_000u64.to_le_bytes()), OK);
        let s = snapshot_vec();
        for _ in 0..200 {
            sim_step();
        }
        let h_end = sim_hash();
        restore_vec(&s);
        assert_eq!(sim_tick(), 1_000_000);
        for _ in 0..200 {
            sim_step();
        }
        assert_eq!(sim_hash(), h_end);
    }

    #[test]
    fn snapshot_round_trip_resumes_identically_mid_journey() {
        let _g = setup(7);
        let j = journal();
        // tick 60 is mid-movement for dog 3 (and likely dog 7): the
        // snapshot must capture (pos, waypoint, target) well enough that
        // the continuation is bit-identical
        run_journal(&j[..7], 60);
        let s = snapshot_vec();
        let h_mid = sim_hash();
        for _ in 0..300 {
            sim_step();
        }
        let h_end = sim_hash();
        restore_vec(&s);
        assert_eq!(sim_hash(), h_mid);
        for _ in 0..300 {
            sim_step();
        }
        assert_eq!(sim_hash(), h_end);
    }

    #[test]
    fn move_to_walks_the_dog_there_and_then_it_rests() {
        let _g = setup(3);
        assert_eq!(ev_id(EV_JOIN, 8), OK);
        let blob = park_blob();
        let t = Terrain::parse(&blob).unwrap();
        let goal = t.idx(10, 2);
        assert_eq!(ev_move(8, Node::ground(goal).0), OK);
        for _ in 0..2000 {
            sim_step();
            let d = dog(8);
            if d.target == NONE.0 && (d.x >> 16, d.y >> 16) == (10, 2) {
                return;
            }
        }
        let d = dog(8);
        panic!("dog never arrived: at ({}, {})", d.x >> 16, d.y >> 16);
    }

    #[test]
    fn crossing_the_river_uses_the_bridge_surface() {
        let _g = setup(3);
        assert_eq!(ev_id(EV_JOIN, 8), OK);
        // pin the start deterministically: move it to the west side first
        let blob = park_blob();
        let t = Terrain::parse(&blob).unwrap();
        {
            let p = park();
            let i = find(p, 8).unwrap();
            let (x, y) = nav::center(&t, Node::ground(t.idx(1, 5)));
            p.dogs[i].x = x;
            p.dogs[i].y = y;
        }
        assert_eq!(ev_move(8, Node::ground(t.idx(11, 5)).0), OK);
        let mut was_on_deck = false;
        for _ in 0..2000 {
            sim_step();
            let d = dog(8);
            was_on_deck |= d.flags & FLAG_DECK != 0;
            if d.target == NONE.0 && (d.x >> 16) == 11 {
                assert!(
                    was_on_deck,
                    "east crossing at the bridge row should board the deck"
                );
                assert_eq!(d.flags & FLAG_DECK, 0, "arrived back on ground");
                return;
            }
        }
        panic!("dog never crossed");
    }

    #[test]
    fn boost_doubles_ground_covered() {
        // Two identical walks along the open south row, one boosted: after
        // the same tick budget the boosted dog must be measurably ahead —
        // and exactly at double speed until arrival (WALK_SPEED is exact).
        let blob = park_blob();
        let t = Terrain::parse(&blob).unwrap();
        let start = Node::ground(t.idx(0, 10));
        let goal = Node::ground(t.idx(5, 10)).0;
        let run = |boosted: bool| -> i32 {
            let _g = setup(77);
            assert_eq!(ev_id(EV_JOIN, 8), OK);
            {
                let p = park();
                let i = find(p, 8).unwrap();
                let (x, y) = nav::center(&t, start);
                p.dogs[i].x = x;
                p.dogs[i].y = y;
            }
            if boosted {
                assert_eq!(ev_boost(8, 1), OK);
            }
            assert_eq!(ev_move(8, goal), OK);
            // 16 ticks: 2 cells unboosted, 4 boosted — neither arrives on
            // the 5-cell course, so the differential is pure speed.
            for _ in 0..16 {
                sim_step();
            }
            dog(8).x
        };
        let slow = run(false);
        let fast = run(true);
        let (sx, _) = nav::center(&t, start);
        assert_eq!(
            fast - sx,
            2 * (slow - sx),
            "boosted dog should cover exactly double the distance"
        );
        assert!(fast > slow);
    }

    #[test]
    fn held_boost_patrols_the_bridge_and_release_stops_rearming() {
        let _g = setup(31);
        assert_eq!(ev_id(EV_JOIN, 6), OK);
        let blob = park_blob();
        let t = Terrain::parse(&blob).unwrap();
        // west platform of the deck span (5..7, 5): the patrol endpoints
        // resolve to the platforms at (4,5) and (8,5)
        {
            let p = park();
            let i = find(p, 6).unwrap();
            let (x, y) = nav::center(&t, Node::ground(t.idx(4, 5)));
            p.dogs[i].x = x;
            p.dogs[i].y = y;
        }
        assert_eq!(ev_boost(6, 1), OK);
        let mut crossed_deck = false;
        let mut reached_east = false;
        let mut returned_west = false;
        for _ in 0..1200 {
            sim_step();
            let d = dog(6);
            crossed_deck |= d.flags & FLAG_DECK != 0;
            let cell = ((d.x >> 16), (d.y >> 16));
            if cell == (8, 5) {
                reached_east = true;
            }
            if reached_east && cell == (4, 5) {
                returned_west = true;
                break;
            }
        }
        assert!(crossed_deck, "patrol must cross the deck");
        assert!(reached_east && returned_west, "patrol must ping-pong");
        // release mid-leg: the current leg finishes, no new leg re-arms
        assert_eq!(ev_boost(6, 0), OK);
        for _ in 0..2000 {
            sim_step();
            if dog(6).target == NONE.0 {
                break;
            }
        }
        assert_eq!(dog(6).target, NONE.0, "leg should finish");
        sim_step();
        assert_eq!(dog(6).target, NONE.0, "patrol must not re-arm unboosted");
    }

    #[test]
    fn boost_transitions_gate_and_survive_snapshots() {
        let _g = setup(21);
        assert_eq!(ev_id(EV_JOIN, 4), OK);
        assert_eq!(ev_boost(4, 1), OK);
        assert_eq!(ev_boost(4, 1), ERR_NOOP); // held-button resend
        let s = snapshot_vec();
        restore_vec(&s);
        assert_ne!(dog(4).flags & FLAG_BOOST, 0, "boost must survive restore");
        assert_eq!(ev_boost(4, 0), OK);
        assert_eq!(ev_boost(4, 0), ERR_NOOP);
        assert_eq!(ev_boost(9, 1), ERR_ABSENT);
        assert_eq!(ev_boost(4, 2), ERR_ENCODING);
        // an out-of-contract flag bit in a snapshot is refused on restore
        let mut bad = snapshot_vec();
        bad[HEADER3 + 28] |= 0x04;
        io()[..bad.len()].copy_from_slice(&bad);
        assert_eq!(sim_restore(bad.len() as u32), 5);
    }

    #[test]
    fn wander_moves_idle_dogs_deterministically() {
        let _g = setup(1234);
        assert_eq!(ev_id(EV_JOIN, 5), OK);
        assert_eq!(ev_id(EV_JOIN, 9), OK);
        let before: Vec<(i32, i32)> = park().dogs[..2].iter().map(|d| (d.x, d.y)).collect();
        for _ in 0..1200 {
            sim_step();
        }
        let after: Vec<(i32, i32)> = park().dogs[..2].iter().map(|d| (d.x, d.y)).collect();
        assert_ne!(before, after, "idle dogs should wander within a minute");
        let h = sim_hash();
        assert_eq!(sim_init(1234, 42, 1), OK);
        assert_eq!(ev_id(EV_JOIN, 5), OK);
        assert_eq!(ev_id(EV_JOIN, 9), OK);
        for _ in 0..1200 {
            sim_step();
        }
        assert_eq!(sim_hash(), h);
    }

    #[test]
    fn rejects_do_not_mutate() {
        let _g = setup(1);
        assert_eq!(ev_id(EV_JOIN, 5), OK);
        assert_eq!(ev_id(EV_CHECK_IN, 5), OK);
        let s = snapshot_vec();
        let h = sim_hash();
        assert_eq!(ev_id(EV_JOIN, 5), ERR_PRESENT);
        assert_eq!(ev_id(EV_LEAVE, 6), ERR_ABSENT);
        assert_eq!(ev_id(EV_CHECK_IN, 5), ERR_CHECKED_IN);
        assert_eq!(ev_id(EV_CHECK_IN, 9), ERR_ABSENT);
        assert_eq!(ev(99, &5u64.to_le_bytes()), ERR_KIND);
        assert_eq!(ev(EV_JOIN, &[1, 2]), ERR_ENCODING);
        assert_eq!(ev_move(5, 0xFFFE), ERR_TARGET); // out-of-range node
        // aliased encoding (bit 15) of a perfectly standable cell: must be
        // rejected, or first_waypoint would chase stale scratch state
        assert_eq!(ev_move(5, 0x8000), ERR_TARGET);
        assert_eq!(ev_move(6, 0), ERR_ABSENT);
        assert_eq!(ev_boost(5, 0), ERR_NOOP); // already off
        assert_eq!(ev_boost(6, 1), ERR_ABSENT);
        assert_eq!(ev_boost(5, 7), ERR_ENCODING);
        {
            let mut p = 1u32.to_le_bytes().to_vec(); // not > current epoch
            p.extend_from_slice(&0u64.to_le_bytes());
            assert_eq!(ev(EV_EPOCH_ADVANCE, &p), ERR_EPOCH);
        }
        {
            let mut p = mythra_sim_terrain::SCHEMA.to_le_bytes().to_vec();
            p.extend_from_slice(&0xDEAD_BEEFu64.to_le_bytes()); // unknown id
            assert_eq!(ev(EV_TERRAIN_SET, &p), ERR_TERRAIN);
        }
        assert_eq!(sim_hash(), h);
        assert_eq!(snapshot_vec(), s);
    }

    #[test]
    fn check_in_gates_by_day_and_accrues_energy() {
        let _g = setup(3);
        assert_eq!(ev_id(EV_JOIN, 8), OK);
        assert_eq!(ev_id(EV_CHECK_IN, 8), OK);
        assert_eq!(ev_id(EV_CHECK_IN, 8), ERR_CHECKED_IN);
        assert_eq!(ev(EV_DAY_RESET, &1u32.to_le_bytes()), OK);
        assert_eq!(ev_id(EV_CHECK_IN, 8), OK);
        let s = snapshot_vec();
        restore_vec(&s);
        assert_eq!(snapshot_vec(), s);
        assert_eq!(park().energy, 2 * CHECK_IN_ENERGY as u64);
    }

    #[test]
    fn roster_stays_sorted_and_spawns_are_standable() {
        let _g = setup(5);
        let blob = park_blob();
        let t = Terrain::parse(&blob).unwrap();
        for id in [900u64, 3, 77, 500, 12] {
            assert_eq!(ev_id(EV_JOIN, id), OK);
        }
        let p = park();
        for i in 1..p.n {
            assert!(p.dogs[i - 1].id < p.dogs[i].id);
        }
        for d in &p.dogs[..p.n] {
            let idx = t.idx(d.x >> 16, d.y >> 16);
            assert_ne!(t.class(idx), BLOCK, "dog {} spawned in a wall", d.id);
            assert_eq!(d.flags & FLAG_DECK, 0);
        }
        assert_eq!(ev_id(EV_LEAVE, 77), OK);
        let p = park();
        assert_eq!(p.n, 4);
        for i in 1..p.n {
            assert!(p.dogs[i - 1].id < p.dogs[i].id);
        }
    }

    #[test]
    fn hud_reports_the_park_and_the_dog_asking() {
        let _g = setup(3);
        let hud = |id: u64| -> Vec<u8> {
            let len = sim_hud(id) as usize;
            assert_eq!(len, 28);
            io()[..len].to_vec()
        };
        let u32at = |b: &[u8], at: usize| u32::from_le_bytes(b[at..at + 4].try_into().unwrap());
        let u64at = |b: &[u8], at: usize| u64::from_le_bytes(b[at..at + 8].try_into().unwrap());

        assert_eq!(ev_id(EV_JOIN, 8), OK);
        assert_eq!(ev_id(EV_JOIN, 9), OK);
        let h = hud(8);
        assert_eq!(u16::from_le_bytes([h[0], h[1]]), 1);
        assert_eq!((h[2], h[3]), (1, 0));
        assert_eq!(u32at(&h, 8), 2);
        assert_eq!(u64at(&h, 12), 0);
        assert_eq!((u32at(&h, 20), h[24]), (0, 0));

        assert_eq!(ev_id(EV_CHECK_IN, 8), OK);
        assert_eq!(ev_boost(8, 1), OK);
        let h = hud(8);
        assert_eq!((h[2], h[3]), (1, 1));
        assert_eq!(u64at(&h, 12), CHECK_IN_ENERGY as u64);
        assert_eq!(u32at(&h, 20), CHECK_IN_ENERGY);
        assert_eq!(h[24], FLAG_BOOST);

        // a new day re-arms the check-in without touching the energy
        assert_eq!(ev(EV_DAY_RESET, &4u32.to_le_bytes()), OK);
        let h = hud(8);
        assert_eq!((h[2], h[3]), (1, 0));
        assert_eq!(u32at(&h, 4), 4);

        // a dog that isn't here still sees the park
        let h = hud(999);
        assert_eq!((h[2], h[3]), (0, 0));
        assert_eq!(u32at(&h, 8), 2);
        assert_eq!(u64at(&h, 12), CHECK_IN_ENERGY as u64);
        assert_eq!((u32at(&h, 20), h[24]), (0, 0));
        assert_eq!(&h[25..28], &[0, 0, 0]);
    }

    #[test]
    fn full_park_snapshot_fits_the_io_buffer() {
        let _g = setup(6);
        for id in 0..MAX_DOGS as u64 {
            assert_eq!(ev_id(EV_JOIN, id + 1), OK);
        }
        assert_eq!(ev_id(EV_JOIN, 5000), ERR_FULL);
        let len = sim_snapshot() as usize;
        assert_eq!(len, HEADER3 + MAX_DOGS * DOG_REC);
        assert!(len <= IO_CAP);
        let view_len = sim_view() as usize;
        assert_eq!(view_len, 4 + MAX_DOGS * 20);
        assert!(view_len <= IO_CAP);
    }

    #[test]
    fn restore_rejects_malformed_and_mismatched_snapshots() {
        let _g = setup(2);
        assert_eq!(ev_id(EV_JOIN, 1), OK);
        assert_eq!(ev_id(EV_JOIN, 2), OK);
        let s = snapshot_vec();
        let mut bad = s.clone();
        bad[0] = b'X';
        io()[..bad.len()].copy_from_slice(&bad);
        assert_eq!(sim_restore(bad.len() as u32), 1);
        // swap the two dog records: ids out of order
        let mut unsorted = s.clone();
        let (a, b) = (HEADER3, HEADER3 + DOG_REC);
        let rec: Vec<u8> = unsorted[a..a + DOG_REC].to_vec();
        unsorted.copy_within(b..b + DOG_REC, a);
        unsorted[b..b + DOG_REC].copy_from_slice(&rec);
        io()[..unsorted.len()].copy_from_slice(&unsorted);
        assert_eq!(sim_restore(unsorted.len() as u32), 3);
        // truncated record area
        io()[..s.len() - 1].copy_from_slice(&s[..s.len() - 1]);
        assert_eq!(sim_restore((s.len() - 1) as u32), 2);
        // a snapshot taken on different terrain is refused
        let mut other = s.clone();
        other[52] ^= 0xFF; // terrain_id
        io()[..other.len()].copy_from_slice(&other);
        assert_eq!(sim_restore(other.len() as u32), 4);
        // a dog standing outside the world is refused
        let mut oob = s.clone();
        oob[HEADER3 + 8..HEADER3 + 12].copy_from_slice(&(200i32 << 16).to_le_bytes());
        io()[..oob.len()].copy_from_slice(&oob);
        assert_eq!(sim_restore(oob.len() as u32), 5);
        // an out-of-range rate is refused
        let mut badrate = s.clone();
        badrate[60..64].copy_from_slice(&0u32.to_le_bytes());
        io()[..badrate.len()].copy_from_slice(&badrate);
        assert_eq!(sim_restore(badrate.len() as u32), 2);
        restore_vec(&s);
        assert_eq!(snapshot_vec(), s);
    }

    #[test]
    fn terrain_set_relocates_stranded_dogs_and_clears_intent() {
        let _g = setup(11);
        assert_eq!(ev_id(EV_JOIN, 4), OK);
        let blob = park_blob();
        let t = Terrain::parse(&blob).unwrap();
        // park the dog mid-river heading somewhere, then shrink the world
        {
            let p = park();
            let i = find(p, 4).unwrap();
            let (x, y) = nav::center(&t, Node::ground(t.idx(6, 3)));
            p.dogs[i].x = x;
            p.dogs[i].y = y;
            p.dogs[i].target = Node::ground(t.idx(10, 10)).0;
        }
        // new terrain: 8x8, and the old river column (x=6) is now a wall
        let mut buf = vec![0u8; T_HEADER + 64 * BYTES_PER_CELL];
        let mut b = Builder::new(&mut buf, 8, 8);
        for y in 0..8 {
            b.set_ground(6, y, BLOCK, 0);
        }
        let blob2 = b.finish().to_vec();
        load_terrain(&blob2);
        let mut p = mythra_sim_terrain::SCHEMA.to_le_bytes().to_vec();
        p.extend_from_slice(&blob_id(&blob2).to_le_bytes());
        assert_eq!(ev(EV_TERRAIN_SET, &p), OK);
        let d = dog(4);
        let t2 = Terrain::parse(&blob2).unwrap();
        assert_ne!(t2.class(t2.idx(d.x >> 16, d.y >> 16)), BLOCK);
        assert!(t2.in_bounds(d.x >> 16, d.y >> 16));
        assert_eq!(d.target, NONE.0);
        assert_eq!(sim_terrain_id(), blob_id(&blob2));
        // hash reflects the new world identity
        let h = sim_hash();
        assert_ne!(h, 0);
        // and the whole dance replays identically
        let s = snapshot_vec();
        restore_vec(&s);
        assert_eq!(sim_hash(), h);
    }

    /// The replica announces every mutation of the world that is not a
    /// step, and it knows which events those are from this list. A new
    /// kind that relocates a dog and is not announced moves the rendered
    /// world silently on every host that reads it, which is a jump on
    /// screen with nothing to absorb it — so the list is pinned here,
    /// where the rule actually lives.
    #[test]
    fn no_event_but_a_terrain_change_moves_a_dog_that_is_already_here() {
        let _g = setup(23);
        let blob = park_blob();
        let t = Terrain::parse(&blob).unwrap();
        assert_eq!(ev_id(EV_JOIN, 3), OK);
        assert_eq!(ev_id(EV_JOIN, 7), OK);
        for _ in 0..40 {
            sim_step();
        }
        let was = {
            let d = dog(3);
            (d.x, d.y)
        };
        let mut check = |kind: u16, code: u32| {
            assert_eq!(code, OK, "event {kind} was refused, so it proves nothing");
            let d = dog(3);
            assert_eq!((d.x, d.y), was, "event {kind} moved a dog with no step");
        };
        check(EV_CHECK_IN, ev_id(EV_CHECK_IN, 3));
        check(EV_MOVE_TO, ev_move(3, Node::ground(t.idx(10, 2)).0));
        check(EV_BOOST_SET, ev_boost(3, 1));
        check(EV_DAY_RESET, ev(EV_DAY_RESET, &1u32.to_le_bytes()));
        check(EV_RATE_SET, ev(EV_RATE_SET, &48u32.to_le_bytes()));
        check(EV_CLOCK_SKIP, ev(EV_CLOCK_SKIP, &10_000u64.to_le_bytes()));
        check(EV_LEAVE, ev_id(EV_LEAVE, 7));
        let mut p = 9u32.to_le_bytes().to_vec();
        p.extend_from_slice(&0u64.to_le_bytes());
        check(EV_EPOCH_ADVANCE, ev(EV_EPOCH_ADVANCE, &p));
        // A join places a dog nobody has seen yet, which has no position
        // to keep, and is the only other apply that writes one.
        assert_eq!(ev_id(EV_JOIN, 11), OK);
        let d = dog(3);
        assert_eq!((d.x, d.y), was);
    }

    #[test]
    fn init_and_apply_require_terrain() {
        let _g = LOCK.lock().unwrap_or_else(|e| e.into_inner());
        unsafe { *(&raw mut TERRAIN) = None };
        assert_eq!(sim_init(1, 1, 1), ERR_TERRAIN);
        load_terrain(&park_blob());
        assert_eq!(sim_init(1, 1, 1), OK);
    }
}

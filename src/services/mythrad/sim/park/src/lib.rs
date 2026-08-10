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
//!
//! ABI (all integers; payloads move through the io buffer):
//!   io_buf() -> *mut u8, io_cap() -> u32
//!   sim_init(seed: u64, park_id: u64, epoch: u32)
//!   sim_restore(len: u32) -> u32      0 ok, else restore error code
//!   sim_snapshot() -> u32             canonical snapshot written to io, len
//!   sim_step()
//!   sim_apply(len: u32) -> u32        0 ok, else reject code
//!   sim_hash() -> u64                 canonical state hash
//!   sim_tick() -> u64, sim_epoch() -> u32
//!   sim_view() -> u32                 render data written to io, len
//!
//! Event encoding (little-endian): kind u16, then payload —
//!   1 join          { id u64 }            spawn position derived from
//!                                         det_rand(seed, tick, id)
//!   2 leave         { id u64 }
//!   3 check_in      { id u64 }            once per day index per dog
//!   4 chat          { id u64, len u16, utf8 }  validated, no state change
//!   5 day_reset     { day u32 }           system event; re-arms check-ins
//!   6 epoch_advance { epoch u32, module_hash u64 }  system event
//!
//! Snapshot encoding (canonical; dogs strictly sorted by id; park energy
//! is cumulative — departed dogs' contributions persist — so it is state,
//! not a derivable aggregate):
//!   magic "MYP1", epoch u32, park_id u64, seed u64, tick u64, day u32,
//!   n u32, energy u64, then n * { id u64, x i32, y i32, energy u32,
//!   checked_in_day u32 }
#![cfg_attr(not(test), no_std)]

use mythra_sim_core::{det_rand, step_dog, GRID_H, GRID_W};

#[cfg(target_arch = "wasm32")]
#[panic_handler]
fn panic(_: &core::panic::PanicInfo) -> ! {
    core::arch::wasm32::unreachable()
}

pub const MAX_DOGS: usize = 2048;
const IO_CAP: usize = 64 * 1024;
const MAGIC: u32 = u32::from_le_bytes(*b"MYP1");
const NEVER: u32 = u32::MAX;
const CHAT_MAX: usize = 280;
const DOG_REC: usize = 24;
const HEADER: usize = 48;

pub const EV_JOIN: u16 = 1;
pub const EV_LEAVE: u16 = 2;
pub const EV_CHECK_IN: u16 = 3;
pub const EV_CHAT: u16 = 4;
pub const EV_DAY_RESET: u16 = 5;
pub const EV_EPOCH_ADVANCE: u16 = 6;

pub const OK: u32 = 0;
pub const ERR_ENCODING: u32 = 1;
pub const ERR_PRESENT: u32 = 2;
pub const ERR_ABSENT: u32 = 3;
pub const ERR_FULL: u32 = 4;
pub const ERR_CHECKED_IN: u32 = 5;
pub const ERR_KIND: u32 = 6;
pub const ERR_EPOCH: u32 = 7;

const CHECK_IN_ENERGY: u32 = 10;

#[derive(Clone, Copy)]
struct Dog {
    id: u64,
    x: i32,
    y: i32,
    energy: u32,
    checked_in_day: u32,
}

const EMPTY_DOG: Dog = Dog {
    id: 0,
    x: 0,
    y: 0,
    energy: 0,
    checked_in_day: NEVER,
};

struct Park {
    epoch: u32,
    park_id: u64,
    seed: u64,
    tick: u64,
    day: u32,
    energy: u64,
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
    n: 0,
    dogs: [EMPTY_DOG; MAX_DOGS],
};

static mut IO: [u8; IO_CAP] = [0; IO_CAP];

fn park() -> &'static mut Park {
    unsafe { &mut *(&raw mut PARK) }
}

fn io() -> &'static mut [u8; IO_CAP] {
    unsafe { &mut *(&raw mut IO) }
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
pub extern "C" fn sim_init(seed: u64, park_id: u64, epoch: u32) {
    let p = park();
    *p = Park {
        epoch,
        park_id,
        seed,
        tick: 0,
        day: 0,
        energy: 0,
        n: 0,
        dogs: [EMPTY_DOG; MAX_DOGS],
    };
}

#[unsafe(no_mangle)]
pub extern "C" fn sim_tick() -> u64 {
    park().tick
}

#[unsafe(no_mangle)]
pub extern "C" fn sim_epoch() -> u32 {
    park().epoch
}

/// One tick: every dog steps under the shared core dynamics. Pure function
/// of state; identical on every surface by construction.
#[unsafe(no_mangle)]
pub extern "C" fn sim_step() {
    let p = park();
    for i in 0..p.n {
        let d = &mut p.dogs[i];
        let id = d.id.to_le_bytes();
        let (dx, dy) = step_dog(p.tick, p.seed, &id, d.x, d.y, GRID_W, GRID_H);
        d.x = (d.x + dx).clamp(0, GRID_W - 1);
        d.y = (d.y + dy).clamp(0, GRID_H - 1);
    }
    p.tick += 1;
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
            let Some(id) = read_u64(body, len) else {
                return ERR_ENCODING;
            };
            let p = park();
            if find(p, id).is_some() {
                return ERR_PRESENT;
            }
            if p.n >= MAX_DOGS {
                return ERR_FULL;
            }
            let x = det_rand(p.seed, p.tick, id, GRID_W as u32) as i32;
            let y = det_rand(p.seed, p.tick.wrapping_add(1), id, GRID_H as u32) as i32;
            insert(
                p,
                Dog {
                    id,
                    x,
                    y,
                    energy: 0,
                    checked_in_day: NEVER,
                },
            );
            OK
        }
        EV_LEAVE => {
            let Some(id) = read_u64(body, len) else {
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
            let Some(id) = read_u64(body, len) else {
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
        EV_CHAT => {
            let Some(id) = read_u64(body, len) else {
                return ERR_ENCODING;
            };
            if len < body + 10 {
                return ERR_ENCODING;
            }
            let text_len = {
                let buf = &io()[..len];
                u16::from_le_bytes([buf[body + 8], buf[body + 9]]) as usize
            };
            if text_len > CHAT_MAX || body + 10 + text_len != len {
                return ERR_ENCODING;
            }
            let p = park();
            if find(p, id).is_none() {
                return ERR_ABSENT;
            }
            // Validated and journaled; chat carries no park state.
            OK
        }
        EV_DAY_RESET => {
            let Some(day) = read_u32(body, len) else {
                return ERR_ENCODING;
            };
            park().day = day;
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
        _ => ERR_KIND,
    }
}

fn read_u64(at: usize, len: usize) -> Option<u64> {
    if len < at + 8 {
        return None;
    }
    let buf = &io()[..len];
    let mut b = [0u8; 8];
    b.copy_from_slice(&buf[at..at + 8]);
    Some(u64::from_le_bytes(b))
}

fn read_u32(at: usize, len: usize) -> Option<u32> {
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
    let total = HEADER + n * DOG_REC;
    let buf = io();
    buf[0..4].copy_from_slice(&MAGIC.to_le_bytes());
    buf[4..8].copy_from_slice(&p.epoch.to_le_bytes());
    buf[8..16].copy_from_slice(&p.park_id.to_le_bytes());
    buf[16..24].copy_from_slice(&p.seed.to_le_bytes());
    buf[24..32].copy_from_slice(&p.tick.to_le_bytes());
    buf[32..36].copy_from_slice(&p.day.to_le_bytes());
    buf[36..40].copy_from_slice(&(n as u32).to_le_bytes());
    buf[40..48].copy_from_slice(&p.energy.to_le_bytes());
    let mut at = HEADER;
    for d in &p.dogs[..n] {
        buf[at..at + 8].copy_from_slice(&d.id.to_le_bytes());
        buf[at + 8..at + 12].copy_from_slice(&d.x.to_le_bytes());
        buf[at + 12..at + 16].copy_from_slice(&d.y.to_le_bytes());
        buf[at + 16..at + 20].copy_from_slice(&d.energy.to_le_bytes());
        buf[at + 20..at + 24].copy_from_slice(&d.checked_in_day.to_le_bytes());
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
    let buf = io();
    if u32::from_le_bytes([buf[0], buf[1], buf[2], buf[3]]) != MAGIC {
        return 1;
    }
    let n = u32::from_le_bytes([buf[36], buf[37], buf[38], buf[39]]) as usize;
    if n > MAX_DOGS || len != HEADER + n * DOG_REC {
        return 2;
    }
    let mut dogs = [EMPTY_DOG; MAX_DOGS];
    let mut prev: Option<u64> = None;
    let mut at = HEADER;
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
        *slot = Dog {
            id,
            x: i32::from_le_bytes(rd(8)),
            y: i32::from_le_bytes(rd(12)),
            energy: u32::from_le_bytes(rd(16)),
            checked_in_day: u32::from_le_bytes(rd(20)),
        };
        at += DOG_REC;
    }
    let mut b8 = [0u8; 8];
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
    p.n = n;
    p.dogs = dogs;
    0
}

/// Canonical state hash: FNV-1a over every state field in snapshot order,
/// finalized with a splitmix64 mix. Two replicas holding the same state
/// produce the same value; any single divergence changes it.
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
    for d in &p.dogs[..p.n] {
        h.u64(d.id);
        h.u32(d.x as u32);
        h.u32(d.y as u32);
        h.u32(d.energy);
        h.u32(d.checked_in_day);
    }
    mix64(h.0)
}

#[unsafe(no_mangle)]
pub extern "C" fn sim_view() -> u32 {
    let p = park();
    let n = p.n;
    let buf = io();
    buf[0..4].copy_from_slice(&(n as u32).to_le_bytes());
    let mut at = 4;
    for d in &p.dogs[..n] {
        buf[at..at + 8].copy_from_slice(&d.id.to_le_bytes());
        buf[at + 8..at + 12].copy_from_slice(&d.x.to_le_bytes());
        buf[at + 12..at + 16].copy_from_slice(&d.y.to_le_bytes());
        at += 16;
    }
    at as u32
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

fn mix64(mut z: u64) -> u64 {
    z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
    z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
    z ^ (z >> 31)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::{Mutex, MutexGuard};

    // The module state is a single static (as it is in wasm); tests
    // serialize on this lock and re-init in every case.
    static LOCK: Mutex<()> = Mutex::new(());

    fn setup(seed: u64) -> MutexGuard<'static, ()> {
        let g = LOCK.lock().unwrap_or_else(|e| e.into_inner());
        sim_init(seed, 42, 1);
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

    fn chat(id: u64, text: &[u8]) -> u32 {
        let mut p = Vec::new();
        p.extend_from_slice(&id.to_le_bytes());
        p.extend_from_slice(&(text.len() as u16).to_le_bytes());
        p.extend_from_slice(text);
        ev(EV_CHAT, &p)
    }

    fn snapshot_vec() -> Vec<u8> {
        let len = sim_snapshot() as usize;
        io()[..len].to_vec()
    }

    fn restore_vec(s: &[u8]) {
        io()[..s.len()].copy_from_slice(s);
        assert_eq!(sim_restore(s.len() as u32), 0);
    }

    // A scripted journal: (tick, kind, id-or-value). Applies each event at
    // its tick per the replication contract, stepping between.
    fn run_journal(journal: &[(u64, u16, u64)], until_tick: u64) {
        for &(tick, kind, val) in journal {
            while sim_tick() < tick {
                sim_step();
            }
            let code = match kind {
                EV_DAY_RESET => ev(EV_DAY_RESET, &(val as u32).to_le_bytes()),
                EV_EPOCH_ADVANCE => {
                    let mut p = (val as u32).to_le_bytes().to_vec();
                    p.extend_from_slice(&0u64.to_le_bytes());
                    ev(EV_EPOCH_ADVANCE, &p)
                }
                _ => ev_id(kind, val),
            };
            assert_eq!(code, OK, "event {kind} at tick {tick}");
        }
        while sim_tick() < until_tick {
            sim_step();
        }
    }

    const JOURNAL: &[(u64, u16, u64)] = &[
        (0, EV_JOIN, 7),
        (0, EV_JOIN, 3),
        (5, EV_JOIN, 11),
        (5, EV_CHECK_IN, 7),
        (30, EV_CHECK_IN, 3),
        (100, EV_LEAVE, 7),
        (240, EV_DAY_RESET, 1),
        (241, EV_CHECK_IN, 3),
        (300, EV_EPOCH_ADVANCE, 2),
    ];

    #[test]
    fn replay_is_deterministic() {
        let _g = setup(99);
        run_journal(JOURNAL, 500);
        let h1 = sim_hash();
        let s1 = snapshot_vec();
        sim_init(99, 42, 1);
        run_journal(JOURNAL, 500);
        assert_eq!(sim_hash(), h1);
        assert_eq!(snapshot_vec(), s1);
    }

    #[test]
    fn snapshot_round_trip_resumes_identically() {
        let _g = setup(7);
        run_journal(JOURNAL, 350);
        let s = snapshot_vec();
        let h_mid = sim_hash();
        for _ in 0..150 {
            sim_step();
        }
        let h_end = sim_hash();
        restore_vec(&s);
        assert_eq!(sim_hash(), h_mid);
        for _ in 0..150 {
            sim_step();
        }
        assert_eq!(sim_hash(), h_end);
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
        assert_eq!(chat(6, b"woof"), ERR_ABSENT);
        assert_eq!(ev(99, &5u64.to_le_bytes()), ERR_KIND);
        assert_eq!(ev(EV_JOIN, &[1, 2]), ERR_ENCODING);
        assert_eq!(chat(5, &[b'a'; CHAT_MAX + 1]), ERR_ENCODING);
        {
            let mut p = 1u32.to_le_bytes().to_vec(); // not > current epoch
            p.extend_from_slice(&0u64.to_le_bytes());
            assert_eq!(ev(EV_EPOCH_ADVANCE, &p), ERR_EPOCH);
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
        // header energy is derivable from dog records on restore
        restore_vec(&s);
        assert_eq!(snapshot_vec(), s);
        assert_eq!(park().energy, 2 * CHECK_IN_ENERGY as u64);
    }

    #[test]
    fn roster_stays_sorted_and_bounded() {
        let _g = setup(5);
        for id in [900u64, 3, 77, 500, 12] {
            assert_eq!(ev_id(EV_JOIN, id), OK);
        }
        let p = park();
        for i in 1..p.n {
            assert!(p.dogs[i - 1].id < p.dogs[i].id);
        }
        assert_eq!(ev_id(EV_LEAVE, 77), OK);
        let p = park();
        assert_eq!(p.n, 4);
        for i in 1..p.n {
            assert!(p.dogs[i - 1].id < p.dogs[i].id);
        }
    }

    #[test]
    fn restore_rejects_malformed_snapshots() {
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
        let (a, b) = (HEADER, HEADER + DOG_REC);
        let rec: Vec<u8> = unsorted[a..a + DOG_REC].to_vec();
        unsorted.copy_within(b..b + DOG_REC, a);
        unsorted[b..b + DOG_REC].copy_from_slice(&rec);
        io()[..unsorted.len()].copy_from_slice(&unsorted);
        assert_eq!(sim_restore(unsorted.len() as u32), 3);
        // truncated record area
        io()[..s.len() - 1].copy_from_slice(&s[..s.len() - 1]);
        assert_eq!(sim_restore((s.len() - 1) as u32), 2);
    }

    #[test]
    fn join_spawn_is_deterministic_and_in_bounds() {
        let _g = setup(1234);
        for _ in 0..10 {
            sim_step();
        }
        assert_eq!(ev_id(EV_JOIN, 55), OK);
        let (x1, y1) = {
            let p = park();
            let i = find(p, 55).unwrap();
            (p.dogs[i].x, p.dogs[i].y)
        };
        assert!((0..GRID_W).contains(&x1) && (0..GRID_H).contains(&y1));
        sim_init(1234, 42, 1);
        for _ in 0..10 {
            sim_step();
        }
        assert_eq!(ev_id(EV_JOIN, 55), OK);
        let p = park();
        let i = find(p, 55).unwrap();
        assert_eq!((p.dogs[i].x, p.dogs[i].y), (x1, y1));
    }
}

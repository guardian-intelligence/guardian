//! The toy reference game: the smallest simulation that exercises every
//! part of the chunkies ABI. It exists so the framework can prove — via
//! the same gametest suite WUM passes — that the trait, the macro, and
//! the v2 SimEvent envelope work with zero game-specific help, and so a
//! future game has a complete, boring example to copy.
//!
//! The world: up to 16 actors on a number line. Joining spawns at a
//! seed-derived position; a move nudges by a bounded signed delta; every
//! tick, everyone drifts one unit toward zero. Content-free
//! (content_cap = 0): the host's fetch dance is skipped entirely.

#![cfg_attr(not(test), no_std)]

use chunkies_abi::{OK, Simulation};

/// Game event kinds (the game range starts at 0x0100).
pub const K_JOIN: u16 = 0x0100;
pub const K_MOVE: u16 = 0x0101;
/// Framework-driven system events this game supports.
pub const K_CLOCK_SKIP: u16 = 0x0009;
pub const K_EPOCH_ADVANCE: u16 = 0x0006;
pub const K_RATE_SET: u16 = 0x000A;

/// Game reject codes (must stay below the framework range).
pub const ERR_ENCODING: u32 = 1;
pub const ERR_FULL: u32 = 2;
pub const ERR_PRESENT: u32 = 3;
pub const ERR_ABSENT: u32 = 4;
pub const ERR_NOOP: u32 = 5;
pub const ERR_KIND: u32 = 6;
pub const ERR_BACKWARD: u32 = 7;
pub const ERR_SNAPSHOT: u32 = 8;

pub const MAX_ACTORS: usize = 16;
const GENESIS_HZ: u32 = 24;
const MAGIC: u32 = 0x31594F54; // "TOY1"
// magic u32, seed u64, chunk u64, epoch u32, rate u32, anchor_tick u64,
// anchor_ns u64, tick u64, n u32; then n * (id u64, pos i32).
const SNAP_HEADER: usize = 56;
const ACTOR_REC: usize = 12;

#[derive(Clone, Copy)]
struct Actor {
    id: u64,
    pos: i32,
}

pub struct Toy {
    seed: u64,
    chunk: u64,
    epoch: u32,
    tick: u64,
    rate_hz: u32,
    anchor_tick: u64,
    anchor_ns: u64,
    n: usize,
    actors: [Actor; MAX_ACTORS],
}

const EMPTY: Actor = Actor { id: 0, pos: 0 };

impl Toy {
    fn find(&self, id: u64) -> Option<usize> {
        self.actors[..self.n].iter().position(|a| a.id == id)
    }

    /// Keeps the roster sorted by id so the snapshot is canonical by
    /// construction.
    fn insert(&mut self, id: u64, pos: i32) {
        let at = self.actors[..self.n]
            .iter()
            .position(|a| a.id > id)
            .unwrap_or(self.n);
        for i in (at..self.n).rev() {
            self.actors[i + 1] = self.actors[i];
        }
        self.actors[at] = Actor { id, pos };
        self.n += 1;
    }
}

fn mix64(mut z: u64) -> u64 {
    z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
    z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
    z ^ (z >> 31)
}

fn i32le(b: &[u8]) -> i32 {
    i32::from_le_bytes([b[0], b[1], b[2], b[3]])
}

impl Simulation for Toy {
    const NEW: Toy = Toy {
        seed: 0,
        chunk: 0,
        epoch: 0,
        tick: 0,
        rate_hz: GENESIS_HZ,
        anchor_tick: 0,
        anchor_ns: 0,
        n: 0,
        actors: [EMPTY; MAX_ACTORS],
    };

    fn init(&mut self, seed: u64, chunk: u64, epoch: u32) -> u32 {
        *self = Toy {
            seed,
            chunk,
            epoch,
            ..Toy::NEW
        };
        OK
    }

    fn apply(&mut self, kind: u16, actor: u64, payload: &[u8]) -> u32 {
        match kind {
            K_JOIN => {
                if !payload.is_empty() || actor == 0 {
                    return ERR_ENCODING;
                }
                if self.n == MAX_ACTORS {
                    return ERR_FULL;
                }
                if self.find(actor).is_some() {
                    return ERR_PRESENT;
                }
                let pos = (mix64(self.seed ^ actor) % 201) as i32 - 100;
                self.insert(actor, pos);
                OK
            }
            K_MOVE => {
                if payload.len() != 4 {
                    return ERR_ENCODING;
                }
                let d = i32le(payload);
                if d == 0 || !(-8..=8).contains(&d) {
                    return ERR_NOOP;
                }
                let Some(i) = self.find(actor) else {
                    return ERR_ABSENT;
                };
                self.actors[i].pos = self.actors[i].pos.saturating_add(d);
                OK
            }
            K_CLOCK_SKIP => {
                if payload.len() != 8 || actor != 0 {
                    return ERR_ENCODING;
                }
                let to = u64::from_le_bytes([
                    payload[0], payload[1], payload[2], payload[3], payload[4], payload[5],
                    payload[6], payload[7],
                ]);
                if to <= self.tick {
                    return ERR_BACKWARD;
                }
                self.anchor_ns = self.anchor_ns.wrapping_add(
                    (to - self.anchor_tick).wrapping_mul(1_000_000_000) / self.rate_hz as u64,
                );
                self.anchor_tick = to;
                self.tick = to;
                OK
            }
            K_EPOCH_ADVANCE => {
                if payload.len() != 12 || actor != 0 {
                    return ERR_ENCODING;
                }
                self.epoch = u32::from_le_bytes([payload[0], payload[1], payload[2], payload[3]]);
                OK
            }
            K_RATE_SET => {
                if payload.len() != 4 || actor != 0 {
                    return ERR_ENCODING;
                }
                let hz = u32::from_le_bytes([payload[0], payload[1], payload[2], payload[3]]);
                if hz == 0 || hz == self.rate_hz {
                    return ERR_NOOP;
                }
                self.anchor_ns = self.anchor_ns.wrapping_add(
                    (self.tick - self.anchor_tick).wrapping_mul(1_000_000_000)
                        / self.rate_hz as u64,
                );
                self.anchor_tick = self.tick;
                self.rate_hz = hz;
                OK
            }
            _ => ERR_KIND,
        }
    }

    fn step(&mut self) {
        for a in &mut self.actors[..self.n] {
            a.pos -= a.pos.signum();
        }
        self.tick += 1;
    }

    fn hash(&self) -> u64 {
        let mut h = 0xCBF2_9CE4_8422_2325u64 ^ MAGIC as u64;
        let mut eat = |v: u64| {
            for b in v.to_le_bytes() {
                h ^= b as u64;
                h = h.wrapping_mul(0x0000_0100_0000_01B3);
            }
        };
        eat(self.seed);
        eat(self.chunk);
        eat(self.epoch as u64);
        eat(self.tick);
        eat(self.rate_hz as u64);
        eat(self.anchor_tick);
        eat(self.anchor_ns);
        eat(self.n as u64);
        for a in &self.actors[..self.n] {
            eat(a.id);
            eat(a.pos as u32 as u64);
        }
        mix64(h)
    }

    fn snapshot(&self, dst: &mut [u8]) -> usize {
        let total = SNAP_HEADER + self.n * ACTOR_REC;
        if dst.len() < total {
            return 0;
        }
        let mut at = 0;
        let mut put = |b: &[u8], at: &mut usize| {
            dst[*at..*at + b.len()].copy_from_slice(b);
            *at += b.len();
        };
        put(&MAGIC.to_le_bytes(), &mut at);
        put(&self.seed.to_le_bytes(), &mut at);
        put(&self.chunk.to_le_bytes(), &mut at);
        put(&self.epoch.to_le_bytes(), &mut at);
        put(&self.rate_hz.to_le_bytes(), &mut at);
        put(&self.anchor_tick.to_le_bytes(), &mut at);
        put(&self.anchor_ns.to_le_bytes(), &mut at);
        put(&self.tick.to_le_bytes(), &mut at);
        put(&(self.n as u32).to_le_bytes(), &mut at);
        for a in &self.actors[..self.n] {
            put(&a.id.to_le_bytes(), &mut at);
            put(&a.pos.to_le_bytes(), &mut at);
        }
        at
    }

    fn restore(&mut self, src: &[u8]) -> u32 {
        if src.len() < SNAP_HEADER || i32le(&src[0..4]) as u32 != MAGIC {
            return ERR_SNAPSHOT;
        }
        let u64at = |at: usize| {
            u64::from_le_bytes([
                src[at],
                src[at + 1],
                src[at + 2],
                src[at + 3],
                src[at + 4],
                src[at + 5],
                src[at + 6],
                src[at + 7],
            ])
        };
        let u32at =
            |at: usize| u32::from_le_bytes([src[at], src[at + 1], src[at + 2], src[at + 3]]);
        let n = u32at(52) as usize;
        if n > MAX_ACTORS || src.len() != SNAP_HEADER + n * ACTOR_REC || u32at(24) == 0 {
            return ERR_SNAPSHOT;
        }
        // Canonical form requires strictly ascending, nonzero actor ids.
        let mut last = 0u64;
        for i in 0..n {
            let id = u64at(SNAP_HEADER + i * ACTOR_REC);
            if id == 0 || id <= last {
                return ERR_SNAPSHOT;
            }
            last = id;
        }
        let mut fresh = Toy {
            seed: u64at(4),
            chunk: u64at(12),
            epoch: u32at(20),
            rate_hz: u32at(24),
            anchor_tick: u64at(28),
            anchor_ns: u64at(36),
            tick: u64at(44),
            n,
            actors: [EMPTY; MAX_ACTORS],
        };
        for i in 0..n {
            let at = SNAP_HEADER + i * ACTOR_REC;
            fresh.actors[i] = Actor {
                id: u64at(at),
                pos: i32le(&src[at + 8..at + 12]),
            };
        }
        *self = fresh;
        OK
    }

    fn tick(&self) -> u64 {
        self.tick
    }

    fn epoch(&self) -> u32 {
        self.epoch
    }

    fn rate(&self) -> u32 {
        self.rate_hz
    }

    fn anchor_tick(&self) -> u64 {
        self.anchor_tick
    }

    fn anchor_ns(&self) -> u64 {
        self.anchor_ns
    }
}

chunkies_abi::export_simulation!(type = Toy, io_cap = 4096, content_cap = 0);

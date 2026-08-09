//! The shared structural core of the Mythra world sim. Everything both
//! sides must agree on lives here: grid geometry, dog identity -> ring
//! derivation, the authoritative step dynamics, presentation smoothing,
//! seeded randomness, and the world-state hash. The server module wraps
//! `step_dog` and the hash (authoritative); the client module wraps
//! `smooth` and the hash (presentation + verification). Both compile this
//! exact code to wasm, so every surface computes identical results.
//!
//! Invariant: fixed-point only. All arithmetic is integer; fractional
//! values use Q16.16 (65536 = 1.0). No float types anywhere in this
//! workspace - enforced by a source gate and a wasm binary scan, so the
//! modules stay bit-identical under any runtime, interpreter, or future
//! native port without trusting anyone's floating-point unit.
#![cfg_attr(not(test), no_std)]

pub const GRID_W: i32 = 100;
pub const GRID_H: i32 = 100;

/// 1.0 in Q16.16.
pub const ONE: i64 = 1 << 16;

// Dynamics constants, Q16.16.
const HALF_CELL: i64 = ONE / 2; // r below this recenters deterministically
const RING_PULL: i64 = ONE / 4; // proportional pull toward the ring
const STEP_THRESHOLD: i64 = 22938; // 0.35: axis speeds above it become a step
const BREATH_CELLS: i64 = 4; // ring swells/contracts this many cells
const BREATH_PERIOD: u64 = 252; // ticks per breath (~10.5s at 24Hz)
const ALPHA_MAX: i64 = ONE + ONE / 4; // smoothing may extrapolate to 1.25

/// Authoritative per-dog step: orbit with a breathing ring. Each dog
/// circles the park center on a ring derived from its id; the ring swells
/// and contracts BREATH_CELLS cells over BREATH_PERIOD ticks. Pure integer
/// function of its inputs; outputs are one grid step on each axis. `seed`
/// is the dog park's shared randomness seed - unused by this behavior, but
/// part of the ABI so behaviors that roll dice stay reproducible on every
/// surface.
pub fn step_dog(tick: u64, _seed: u64, id: &[u8], x: i32, y: i32, w: i32, h: i32) -> (i32, i32) {
    if tick % 2 != 0 {
        return (0, 0);
    }
    // offsets from center in Q16.16; (2x - w) halves without rounding loss
    let rx = ((2 * x - w) as i64) << 15;
    let ry = ((2 * y - h) as i64) << 15;
    let r = isqrt_q16(rx * rx + ry * ry);
    if r < HALF_CELL {
        return (1, 0);
    }
    let b0 = id.first().copied().unwrap_or(0) as i64;
    let b1 = id.get(1).copied().unwrap_or(0) as i64;
    let mut ring = (10 + (b0 + b1) % 28) << 16;
    ring += BREATH_CELLS * sin_turns_q16(phase_turns_q16(tick));
    let g = ((ring - r) * RING_PULL >> 16).clamp(-ONE, ONE);
    let ux = (rx << 16) / r; // unit radial, Q16.16
    let uy = (ry << 16) / r;
    let vx = -uy + (g * ux >> 16);
    let vy = ux + (g * uy >> 16);
    (quantize(vx), quantize(vy))
}

fn quantize(v: i64) -> i32 {
    if v > STEP_THRESHOLD {
        1
    } else if v < -STEP_THRESHOLD {
        -1
    } else {
        0
    }
}

/// Presentation smoothing between two authoritative ticks, absorbing
/// device-specific quirks so every screen renders the same world. All
/// positions are Q16.16 cells. `alpha` is the caller's frame phase between
/// ticks in Q16.16, clamped to a small extrapolation window so a late tick
/// doesn't freeze motion; a jump beyond `snap` cells is a teleport (join,
/// room move, resume after a stall) and snaps instead of gliding.
pub fn smooth(px: i32, py: i32, cx: i32, cy: i32, alpha_q16: u32, snap_q16: u32) -> (i32, i32) {
    let (dx, dy) = ((cx - px) as i64, (cy - py) as i64);
    let snap = snap_q16 as i64;
    if dx * dx + dy * dy > snap * snap {
        return (cx, cy);
    }
    let a = (alpha_q16 as i64).clamp(0, ALPHA_MAX);
    (px + ((dx * a) >> 16) as i32, py + ((dy * a) >> 16) as i32)
}

/// Deterministic randomness shared by server and clients: every roll is a
/// pure function of (park seed, tick, entity), so any surface holding the
/// seed reproduces the server's dice exactly. splitmix64 finalizer.
pub fn det_rand(seed: u64, tick: u64, entity: u64, n: u32) -> u32 {
    if n == 0 {
        return 0;
    }
    let z = mix64(
        seed ^ tick.wrapping_mul(0x9E37_79B9_7F4A_7C15)
            ^ entity.wrapping_mul(0xBF58_476D_1CE4_E5B9),
    );
    (z % n as u64) as u32
}

/// World-state hash: the cross-surface oracle. Order-independent over the
/// dogs (xor of per-dog digests), bound to (tick, seed, count), so the
/// server and every client hash the same world to the same value no matter
/// what order the snapshot listed the dogs in.
pub struct WorldHash {
    base: u64,
    acc: u64,
    count: u64,
}

impl WorldHash {
    pub fn new(tick: u64, seed: u64) -> Self {
        WorldHash {
            base: fnv1a(fnv1a(FNV_OFFSET, &tick.to_le_bytes()), &seed.to_le_bytes()),
            acc: 0,
            count: 0,
        }
    }

    pub fn add(&mut self, id: &[u8], x: i32, y: i32) {
        let mut d = fnv1a(FNV_OFFSET, id);
        d = fnv1a(d, &x.to_le_bytes());
        d = fnv1a(d, &y.to_le_bytes());
        self.acc ^= mix64(d);
        self.count += 1;
    }

    pub fn get(&self) -> u64 {
        mix64(self.base ^ self.acc ^ self.count.wrapping_mul(0x9E37_79B9_7F4A_7C15))
    }
}

const FNV_OFFSET: u64 = 0xCBF2_9CE4_8422_2325;

fn fnv1a(mut h: u64, bytes: &[u8]) -> u64 {
    for &b in bytes {
        h ^= b as u64;
        h = h.wrapping_mul(0x0000_0100_0000_01B3);
    }
    h
}

fn mix64(mut z: u64) -> u64 {
    z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
    z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
    z ^ (z >> 31)
}

/// Breath phase as Q16.16 turns in [0, 1).
fn phase_turns_q16(tick: u64) -> i64 {
    (((tick % BREATH_PERIOD) << 16) / BREATH_PERIOD) as i64
}

/// Sine of an angle given in Q16.16 turns, result Q16.16. 256-bucket table
/// with linear interpolation; max error ~3e-4, far below anything the step
/// quantizer (threshold 0.35) can observe.
fn sin_turns_q16(turns_q16: i64) -> i64 {
    let t = (turns_q16 & 0xFFFF) as usize; // wrap to one turn
    let idx = t >> 8;
    let frac = (t & 0xFF) as i64;
    let a = SIN_TURNS[idx] as i64;
    let b = SIN_TURNS[idx + 1] as i64;
    a + ((b - a) * frac >> 8)
}

/// Integer square root of a Q32.32 value, returned as Q16.16
/// (isqrt(v * 2^32) = isqrt(v) * 2^16). Bit-by-bit method: fixed
/// iteration count, no rounding modes, identical everywhere.
fn isqrt_q16(v: i64) -> i64 {
    let mut n = v as u64;
    let mut result: u64 = 0;
    let mut bit: u64 = 1 << 62;
    while bit > n {
        bit >>= 2;
    }
    while bit != 0 {
        if n >= result + bit {
            n -= result + bit;
            result = (result >> 1) + bit;
        } else {
            result >>= 1;
        }
        bit >>= 2;
    }
    result as i64
}

// round(sin(2*pi*i/256) * 65536) for i in 0..=256.
const SIN_TURNS: [i32; 257] = [
    0, 1608, 3216, 4821, 6424, 8022, 9616, 11204, 12785, 14359, 15924, 17479, 19024, 20557, 22078,
    23586, 25080, 26558, 28020, 29466, 30893, 32303, 33692, 35062, 36410, 37736, 39040, 40320,
    41576, 42806, 44011, 45190, 46341, 47464, 48559, 49624, 50660, 51665, 52639, 53581, 54491,
    55368, 56212, 57022, 57798, 58538, 59244, 59914, 60547, 61145, 61705, 62228, 62714, 63162,
    63572, 63944, 64277, 64571, 64827, 65043, 65220, 65358, 65457, 65516, 65536, 65516, 65457,
    65358, 65220, 65043, 64827, 64571, 64277, 63944, 63572, 63162, 62714, 62228, 61705, 61145,
    60547, 59914, 59244, 58538, 57798, 57022, 56212, 55368, 54491, 53581, 52639, 51665, 50660,
    49624, 48559, 47464, 46341, 45190, 44011, 42806, 41576, 40320, 39040, 37736, 36410, 35062,
    33692, 32303, 30893, 29466, 28020, 26558, 25080, 23586, 22078, 20557, 19024, 17479, 15924,
    14359, 12785, 11204, 9616, 8022, 6424, 4821, 3216, 1608, 0, -1608, -3216, -4821, -6424, -8022,
    -9616, -11204, -12785, -14359, -15924, -17479, -19024, -20557, -22078, -23586, -25080, -26558,
    -28020, -29466, -30893, -32303, -33692, -35062, -36410, -37736, -39040, -40320, -41576, -42806,
    -44011, -45190, -46341, -47464, -48559, -49624, -50660, -51665, -52639, -53581, -54491, -55368,
    -56212, -57022, -57798, -58538, -59244, -59914, -60547, -61145, -61705, -62228, -62714, -63162,
    -63572, -63944, -64277, -64571, -64827, -65043, -65220, -65358, -65457, -65516, -65536, -65516,
    -65457, -65358, -65220, -65043, -64827, -64571, -64277, -63944, -63572, -63162, -62714, -62228,
    -61705, -61145, -60547, -59914, -59244, -58538, -57798, -57022, -56212, -55368, -54491, -53581,
    -52639, -51665, -50660, -49624, -48559, -47464, -46341, -45190, -44011, -42806, -41576, -40320,
    -39040, -37736, -36410, -35062, -33692, -32303, -30893, -29466, -28020, -26558, -25080, -23586,
    -22078, -20557, -19024, -17479, -15924, -14359, -12785, -11204, -9616, -8022, -6424, -4821,
    -3216, -1608, 0,
];

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn dogs_settle_onto_breathing_rings() {
        for (id, mut x, mut y) in [(b"ab", 50, 50), (b"q9", 3, 97), (b"m4", 80, 20)] {
            let ring = 10 + (id[0] as i64 + id[1] as i64) % 28;
            for tick in 0..4800u64 {
                let (dx, dy) = step_dog(tick, 7, id, x, y, GRID_W, GRID_H);
                assert!((-1..=1).contains(&dx) && (-1..=1).contains(&dy));
                x = (x + dx).clamp(0, GRID_W - 1);
                y = (y + dy).clamp(0, GRID_H - 1);
            }
            let rx = ((2 * x - GRID_W) as i64) << 15;
            let ry = ((2 * y - GRID_H) as i64) << 15;
            let r = isqrt_q16(rx * rx + ry * ry) >> 16;
            assert!(
                (r - ring).abs() < 7,
                "dog {id:?} ended r={r}, ring {ring} +-{BREATH_CELLS} breath"
            );
        }
    }

    #[test]
    fn smooth_interpolates_and_snaps() {
        let one = ONE as i32;
        let (x, y) = smooth(0, 0, one, one, (ONE / 2) as u32, (8 * ONE) as u32);
        assert_eq!((x, y), (one / 2, one / 2));
        // teleport: farther than snap distance jumps straight to current
        let (x, _) = smooth(0, 0, 50 * one, 0, (ONE / 2) as u32, (8 * ONE) as u32);
        assert_eq!(x, 50 * one);
        // extrapolation window is capped at 1.25
        let (x, _) = smooth(0, 0, one, 0, (9 * ONE) as u32, (8 * ONE) as u32);
        assert_eq!(x, ALPHA_MAX as i32);
    }

    #[test]
    fn sine_is_odd_and_bounded() {
        assert_eq!(sin_turns_q16(0), 0);
        assert_eq!(sin_turns_q16(ONE / 4), 65536);
        assert_eq!(sin_turns_q16(ONE / 2), 0);
        assert_eq!(sin_turns_q16(3 * ONE / 4), -65536);
        for t in (0..(ONE as usize)).step_by(97) {
            assert!(sin_turns_q16(t as i64).abs() <= 65536);
        }
    }

    #[test]
    fn det_rand_is_stable_and_in_range() {
        let a = det_rand(7, 100, 42, 6);
        assert_eq!(a, det_rand(7, 100, 42, 6));
        assert_ne!(
            (0..64)
                .map(|t| det_rand(7, t, 42, 1 << 30) as u64)
                .sum::<u64>(),
            (0..64)
                .map(|t| det_rand(8, t, 42, 1 << 30) as u64)
                .sum::<u64>(),
        );
        for t in 0..1000 {
            assert!(det_rand(7, t, 42, 6) < 6);
        }
    }

    #[test]
    fn world_hash_is_order_independent_and_sensitive() {
        let mut a = WorldHash::new(500, 7);
        a.add(b"dog1", 10, 20);
        a.add(b"dog2", 30, 40);
        let mut b = WorldHash::new(500, 7);
        b.add(b"dog2", 30, 40);
        b.add(b"dog1", 10, 20);
        assert_eq!(a.get(), b.get());
        // any single divergence changes the hash
        let mut c = WorldHash::new(500, 7);
        c.add(b"dog1", 10, 21);
        c.add(b"dog2", 30, 40);
        assert_ne!(a.get(), c.get());
        assert_ne!(a.get(), WorldHash::new(500, 8).get());
        assert_ne!(a.get(), WorldHash::new(501, 7).get());
    }
}

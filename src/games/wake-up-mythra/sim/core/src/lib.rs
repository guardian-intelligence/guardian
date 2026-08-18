//! The core of the Wake Up Mythra world simulation: the fixed-point
//! primitives every other crate in this game builds on — Q16.16 constants,
//! seeded randomness, hashing, and integer square root — and the place game
//! rules belong as they grow. This crate is the game's, not the framework's:
//! chunkies depends on nothing here, and a rule that lives here can never be
//! read from Go or TypeScript.
//!
//! Invariant: fixed-point only. All arithmetic is integer; fractional
//! values use Q16.16 (65536 = 1.0). No float types anywhere in this
//! workspace - enforced by a source gate and a wasm binary scan, so the
//! modules stay bit-identical under any runtime, interpreter, or future
//! native port without trusting anyone's floating-point unit.
#![cfg_attr(not(test), no_std)]

/// 1.0 in Q16.16.
pub const ONE: i64 = 1 << 16;

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

pub fn mix64(mut z: u64) -> u64 {
    z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
    z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
    z ^ (z >> 31)
}

/// FNV-1a over a byte slice: the workspace's one copy of the constants.
pub fn fnv1a(bytes: &[u8]) -> u64 {
    let mut h: u64 = 0xCBF2_9CE4_8422_2325;
    for &b in bytes {
        h ^= b as u64;
        h = h.wrapping_mul(0x0000_0100_0000_01B3);
    }
    h
}

/// Integer square root of a Q32.32 value, returned as Q16.16
/// (isqrt(v * 2^32) = isqrt(v) * 2^16). Bit-by-bit method: fixed
/// iteration count, no rounding modes, identical everywhere.
pub fn isqrt_q16(v: i64) -> i64 {
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

#[cfg(test)]
mod tests {
    use super::*;

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
    fn isqrt_inverts_squares_exactly() {
        for v in [0i64, 1, 2, 3, 100, 65536, 1 << 20, (1 << 30) + 12345] {
            let r = isqrt_q16(v * v);
            assert_eq!(r, v, "isqrt of {v}^2");
        }
        // monotone over a sweep
        let mut prev = 0;
        for v in (0..(1i64 << 40)).step_by(1 << 33) {
            let r = isqrt_q16(v);
            assert!(r >= prev);
            prev = r;
        }
    }
}

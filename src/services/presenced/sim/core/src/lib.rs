//! The shared structural core of the Mythra world sim. Everything both
//! sides must agree on lives here: grid geometry, dog identity -> ring
//! derivation, the authoritative step dynamics, and the presentation
//! smoothing primitives. The server module wraps `step_dog` (authoritative,
//! integer grid); the client module wraps `smooth` (presentation only,
//! never feeds back into world state). Both compile this exact code to
//! wasm, so the client can reason about server motion bit-for-bit.
#![cfg_attr(not(test), no_std)]

pub const GRID_W: i32 = 100;
pub const GRID_H: i32 = 100;

/// Authoritative per-dog step: orbit with a breathing ring. Each dog
/// circles the park center on a ring derived from its id, and the ring
/// swells and contracts +-4 cells on a ~10s cycle. Pure function of its
/// inputs; outputs are one grid step on each axis.
pub fn step_dog(tick: u64, id: &[u8], x: i32, y: i32, w: i32, h: i32) -> (i32, i32) {
    if tick % 2 != 0 {
        return (0, 0);
    }
    let (cx, cy) = (w as f64 / 2.0, h as f64 / 2.0);
    let (rx, ry) = (x as f64 - cx, y as f64 - cy);
    let r = sqrt(rx * rx + ry * ry);
    if r < 0.5 {
        return (1, 0);
    }
    let b0 = id.first().copied().unwrap_or(0) as i32;
    let b1 = id.get(1).copied().unwrap_or(0) as i32;
    let mut ring = (10 + (b0 + b1) % 28) as f64;
    ring += 4.0 * sin(tick as f64 / 40.0);
    let g = ((ring - r) * 0.25).clamp(-1.0, 1.0);
    let vx = -ry / r + g * rx / r;
    let vy = rx / r + g * ry / r;
    (quantize(vx), quantize(vy))
}

fn quantize(v: f64) -> i32 {
    if v > 0.35 {
        1
    } else if v < -0.35 {
        -1
    } else {
        0
    }
}

/// Presentation smoothing between two authoritative ticks, absorbing
/// device-specific quirks so every screen renders the same world:
/// - `alpha` is the caller's frame phase between ticks; clamped to allow
///   a small extrapolation window so a late tick doesn't freeze motion,
///   but never far enough to overshoot a turn visibly.
/// - a jump larger than `snap` cells is a teleport (join, room move,
///   resume after a stall): snap instead of gliding across the park.
pub fn smooth(px: f32, py: f32, cx: f32, cy: f32, alpha: f32, snap: f32) -> (f32, f32) {
    let (dx, dy) = (cx - px, cy - py);
    if dx * dx + dy * dy > snap * snap {
        return (cx, cy);
    }
    let a = alpha.clamp(0.0, 1.25);
    (px + dx * a, py + dy * a)
}

// f64 sin/sqrt without std: fixed-iteration implementations from pure IEEE
// arithmetic, so every module ships identical math regardless of target or
// host libm. sin is argument-reduced Taylor; both are far more precise than
// anything the +-1 step quantizer (threshold 0.35) can observe.
fn sqrt(v: f64) -> f64 {
    if v <= 0.0 {
        return 0.0;
    }
    // exponent-halving bit seed, then fixed Newton steps: deterministic and
    // accurate to ~1 ulp for the grid magnitudes used here.
    let mut g = f64::from_bits((v.to_bits() >> 1) + 0x1FF8_0000_0000_0000);
    for _ in 0..5 {
        g = 0.5 * (g + v / g);
    }
    g
}

fn sin(x: f64) -> f64 {
    // reduce to [-pi, pi], then a 7-term Taylor series: max error there is
    // ~1e-8, far below anything the +-1 step quantizer can observe.
    const PI: f64 = core::f64::consts::PI;
    const TAU: f64 = 2.0 * PI;
    let mut r = x - (x / TAU + 0.5) as i64 as f64 * TAU;
    if r > PI {
        r -= TAU;
    } else if r < -PI {
        r += TAU;
    }
    let r2 = r * r;
    let mut term = r;
    let mut sum = r;
    let mut n = 1.0;
    for _ in 0..7 {
        term *= -r2 / ((n + 1.0) * (n + 2.0));
        sum += term;
        n += 2.0;
    }
    sum
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn dogs_settle_onto_breathing_rings() {
        for (id, mut x, mut y) in [(b"ab", 50, 50), (b"q9", 3, 97), (b"m4", 80, 20)] {
            let ring = (10 + (id[0] as i32 + id[1] as i32) % 28) as f64;
            for tick in 0..4800u64 {
                let (dx, dy) = step_dog(tick, id, x, y, GRID_W, GRID_H);
                assert!((-1..=1).contains(&dx) && (-1..=1).contains(&dy));
                x = (x + dx).clamp(0, GRID_W - 1);
                y = (y + dy).clamp(0, GRID_H - 1);
            }
            let r = {
                let (rx, ry) = (x as f64 - 50.0, y as f64 - 50.0);
                sqrt(rx * rx + ry * ry)
            };
            assert!(
                (r - ring).abs() < 7.0,
                "dog {id:?} ended r={r:.1}, ring {ring} +-4 breath"
            );
        }
    }

    #[test]
    fn smooth_interpolates_and_snaps() {
        let (x, y) = smooth(0.0, 0.0, 1.0, 1.0, 0.5, 8.0);
        assert!((x - 0.5).abs() < 1e-6 && (y - 0.5).abs() < 1e-6);
        // teleport: farther than snap distance jumps straight to current
        let (x, _) = smooth(0.0, 0.0, 50.0, 0.0, 0.5, 8.0);
        assert_eq!(x, 50.0);
        // extrapolation window is capped
        let (x, _) = smooth(0.0, 0.0, 1.0, 0.0, 9.0, 8.0);
        assert!((x - 1.25).abs() < 1e-6);
    }
}

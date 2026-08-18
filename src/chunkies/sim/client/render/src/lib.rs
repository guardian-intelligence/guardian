//! Presentation smoothing: the host's, never the game's.
//!
//! docs/netcode.md puts render smoothing in `sim/client`, and it belongs
//! there for a structural reason -- it interpolates between two
//! authoritative ticks for display only and never feeds back into world
//! state, so it can never influence `sim_hash`. It lived in a game's core
//! crate, which made the framework depend on a game.
//!
//! Fixed-point only, like every other sim crate: no float types anywhere,
//! enforced by the gate in //src/chunkies/sim.
#![cfg_attr(not(test), no_std)]

/// 1.0 in Q16.16. The wire carries positions in Q16.16 cells; this is the
/// framework's own copy of that protocol constant, not a game's.
pub const ONE: i64 = 1 << 16;

/// Smoothing may extrapolate to 1.25 of a tick.
const ALPHA_MAX: i64 = ONE + ONE / 4;

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

#[cfg(test)]
mod tests {
    use super::*;

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
}

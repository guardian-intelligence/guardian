//! Client presentation module: per-frame smoothing over the shared core,
//! exported for the browser (and later the app's interpreter). The page
//! writes one Q16.16 quad per dog into the frame buffer, calls
//! smooth_frame with its frame phase, and reads the presented positions
//! back from the first two slots of each quad. Presentation only —
//! interpolation, corrections, and every other smoothing concern live
//! exclusively on this side of the wire; nothing here ever feeds back
//! into world state.
//!
//! ABI (all integers; fractional values Q16.16):
//!   frame_cap() -> u32             max dogs per frame
//!   frame_buf() -> *mut i32        quads of [prev_x, prev_y, curr_x, curr_y]
//!   smooth_frame(n: u32, alpha_q16: u32, snap_q16: u32)
//!                                  rewrites slots 0,1 of each quad in place
//!
//! Clock discipline (mythra_sim_clock; the host executes directives and
//! never does time arithmetic itself):
//!   clock_sample(send_ms u64, recv_ms u64, server_tick u64)  verdict echo
//!   clock_reset(server_tick u64, now_ms u64)   after restore/module swap
//!   clock_frame(now_ms u64, replica_tick u64, budget_us u32) -> u32
//!                                  low 16 bits: sim steps to run now;
//!                                  bit 16: request a snapshot resync
//!   clock_phase() -> u32           Q16 frame phase (render alpha)
//!   clock_state() -> u32           0 acquiring, 1 locked, 2 fast-forward,
//!                                  3 snapshot-required (diagnostics)
#![no_std]

use mythra_sim_clock::{Clock, State};

#[cfg(target_arch = "wasm32")]
#[panic_handler]
fn panic(_: &core::panic::PanicInfo) -> ! {
    core::arch::wasm32::unreachable()
}

const MAX_DOGS: usize = 2048;
static mut FRAME: [i32; MAX_DOGS * 4] = [0; MAX_DOGS * 4];
static mut CLOCK: Clock = Clock::NEW;

fn clock() -> &'static mut Clock {
    unsafe { &mut *(&raw mut CLOCK) }
}

#[unsafe(no_mangle)]
pub extern "C" fn clock_sample(send_ms: u64, recv_ms: u64, server_tick: u64) {
    clock().sample(send_ms, recv_ms, server_tick);
}

#[unsafe(no_mangle)]
pub extern "C" fn clock_reset(server_tick: u64, now_ms: u64) {
    clock().reset(server_tick, now_ms);
}

#[unsafe(no_mangle)]
pub extern "C" fn clock_frame(now_ms: u64, replica_tick: u64, budget_us: u32) -> u32 {
    clock().frame(now_ms, replica_tick, budget_us)
}

#[unsafe(no_mangle)]
pub extern "C" fn clock_phase() -> u32 {
    clock().phase_q16()
}

#[unsafe(no_mangle)]
pub extern "C" fn clock_state() -> u32 {
    match clock().state() {
        State::Acquiring => 0,
        State::Locked => 1,
        State::FastForward => 2,
        State::SnapshotRequired => 3,
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn clock_error_q16(now_ms: u64, replica_tick: u64) -> i64 {
    clock().error_q16(now_ms, replica_tick)
}

#[unsafe(no_mangle)]
pub extern "C" fn clock_rtt_ms() -> u32 {
    (clock().rtt_ms_q16() >> 16) as u32
}

#[unsafe(no_mangle)]
pub extern "C" fn frame_cap() -> u32 {
    MAX_DOGS as u32
}

#[unsafe(no_mangle)]
pub extern "C" fn frame_buf() -> *mut i32 {
    &raw mut FRAME as *mut i32
}

#[unsafe(no_mangle)]
pub extern "C" fn smooth_frame(n: u32, alpha_q16: u32, snap_q16: u32) {
    let n = (n as usize).min(MAX_DOGS);
    let frame = unsafe { core::slice::from_raw_parts_mut(&raw mut FRAME as *mut i32, n * 4) };
    for quad in frame.chunks_exact_mut(4) {
        let (sx, sy) =
            mythra_sim_core::smooth(quad[0], quad[1], quad[2], quad[3], alpha_q16, snap_q16);
        quad[0] = sx;
        quad[1] = sy;
    }
}

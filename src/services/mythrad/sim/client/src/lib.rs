//! Client presentation module: per-frame smoothing and the world-hash
//! oracle over the shared core, exported for the browser (and later the
//! app's interpreter). The page writes one Q16.16 quad per dog into the
//! frame buffer, calls smooth_frame with its frame phase, and reads the
//! presented positions back from the first two slots of each quad.
//! Presentation only - nothing here ever feeds back into world state.
//! The wh_* exports let every client re-derive the server's world hash
//! from its own received snapshot: the cross-surface determinism oracle.
//!
//! ABI (all integers; fractional values Q16.16):
//!   frame_cap() -> u32             max dogs per frame
//!   frame_buf() -> *mut i32        quads of [prev_x, prev_y, curr_x, curr_y]
//!   smooth_frame(n: u32, alpha_q16: u32, snap_q16: u32)
//!                                  rewrites slots 0,1 of each quad in place
//!   id_buf() -> *mut u8            64-byte scratch; host writes entity ids
//!   wh_reset(tick: u64, seed: u64) begin a world-hash accumulation
//!   wh_add(id_len: u32, x: i32, y: i32)   fold in one dog (id via id_buf)
//!   wh_get() -> u64                the oracle value
#![no_std]

use mythra_sim_core::WorldHash;

#[cfg(target_arch = "wasm32")]
#[panic_handler]
fn panic(_: &core::panic::PanicInfo) -> ! {
    core::arch::wasm32::unreachable()
}

const MAX_DOGS: usize = 512;
static mut FRAME: [i32; MAX_DOGS * 4] = [0; MAX_DOGS * 4];

const ID_CAP: usize = 64;
static mut ID_BUF: [u8; ID_CAP] = [0; ID_CAP];
static mut WH: Option<WorldHash> = None;

fn id_slice(id_len: u32) -> &'static [u8] {
    let len = (id_len as usize).min(ID_CAP);
    unsafe { core::slice::from_raw_parts(&raw const ID_BUF as *const u8, len) }
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

#[unsafe(no_mangle)]
pub extern "C" fn id_buf() -> *mut u8 {
    &raw mut ID_BUF as *mut u8
}

#[unsafe(no_mangle)]
pub extern "C" fn wh_reset(tick: u64, seed: u64) {
    unsafe { WH = Some(WorldHash::new(tick, seed)) }
}

#[unsafe(no_mangle)]
pub extern "C" fn wh_add(id_len: u32, x: i32, y: i32) {
    if let Some(wh) = unsafe { &mut *(&raw mut WH) } {
        wh.add(id_slice(id_len), x, y);
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn wh_get() -> u64 {
    match unsafe { &*(&raw const WH) } {
        Some(wh) => wh.get(),
        None => 0,
    }
}

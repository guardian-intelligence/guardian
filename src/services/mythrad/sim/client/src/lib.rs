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
#![no_std]

#[cfg(target_arch = "wasm32")]
#[panic_handler]
fn panic(_: &core::panic::PanicInfo) -> ! {
    core::arch::wasm32::unreachable()
}

const MAX_DOGS: usize = 2048;
static mut FRAME: [i32; MAX_DOGS * 4] = [0; MAX_DOGS * 4];

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

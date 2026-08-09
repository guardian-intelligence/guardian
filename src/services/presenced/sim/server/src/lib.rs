//! Server behavior module: the authoritative `step` over the shared core,
//! exported through a host-agnostic ABI for wazero. The module imports
//! nothing - no WASI, no host functions - so a behavior can compute but
//! never reach out, and identical modules in the live and shadow slots
//! diff to zero divergence by construction.
//!
//! ABI:
//!   id_buf() -> *mut u8            64-byte scratch; host writes the dog id
//!   step(tick: u64, id_len: u32, x: i32, y: i32, w: i32, h: i32) -> u64
//!                                  packed (dx as u32) << 32 | (dy as u32)
#![no_std]

#[cfg(target_arch = "wasm32")]
#[panic_handler]
fn panic(_: &core::panic::PanicInfo) -> ! {
    core::arch::wasm32::unreachable()
}

const ID_CAP: usize = 64;
static mut ID_BUF: [u8; ID_CAP] = [0; ID_CAP];

#[unsafe(no_mangle)]
pub extern "C" fn id_buf() -> *mut u8 {
    &raw mut ID_BUF as *mut u8
}

#[unsafe(no_mangle)]
pub extern "C" fn step(tick: u64, id_len: u32, x: i32, y: i32, w: i32, h: i32) -> u64 {
    let len = (id_len as usize).min(ID_CAP);
    let id = unsafe { core::slice::from_raw_parts(&raw const ID_BUF as *const u8, len) };
    let (dx, dy) = mythra_sim_core::step_dog(tick, id, x, y, w, h);
    ((dx as u32 as u64) << 32) | (dy as u32 as u64)
}

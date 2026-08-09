//! Server behavior module: the authoritative `step` and the world-hash
//! oracle over the shared core, exported through a host-agnostic ABI for
//! wazero. The module imports nothing - no WASI, no host functions - so a
//! behavior can compute but never reach out, and identical modules in the
//! live and shadow slots diff to zero divergence by construction.
//!
//! ABI (all integers; positions are grid cells, hashes u64):
//!   id_buf() -> *mut u8            64-byte scratch; host writes entity ids
//!   set_seed(seed: u64)            the park's shared randomness seed; kept
//!                                  out of step's signature so the step
//!                                  arity never changes across module and
//!                                  binary generations rolling independently
//!   step(tick: u64, id_len: u32, x: i32, y: i32, w: i32, h: i32) -> u64
//!                                  packed (dx as u32) << 32 | (dy as u32)
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

const ID_CAP: usize = 64;
static mut ID_BUF: [u8; ID_CAP] = [0; ID_CAP];
static mut SEED: u64 = 0;
static mut WH: Option<WorldHash> = None;

fn id_slice(id_len: u32) -> &'static [u8] {
    let len = (id_len as usize).min(ID_CAP);
    unsafe { core::slice::from_raw_parts(&raw const ID_BUF as *const u8, len) }
}

#[unsafe(no_mangle)]
pub extern "C" fn id_buf() -> *mut u8 {
    &raw mut ID_BUF as *mut u8
}

#[unsafe(no_mangle)]
pub extern "C" fn set_seed(seed: u64) {
    unsafe { SEED = seed }
}

#[unsafe(no_mangle)]
pub extern "C" fn step(tick: u64, id_len: u32, x: i32, y: i32, w: i32, h: i32) -> u64 {
    let seed = unsafe { SEED };
    let (dx, dy) = mythra_sim_core::step_dog(tick, seed, id_slice(id_len), x, y, w, h);
    ((dx as u32 as u64) << 32) | (dy as u32 as u64)
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

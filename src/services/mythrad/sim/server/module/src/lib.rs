//! The live/shadow behavior-slot module. The slot machinery (ConfigMap
//! hot-reload, hash metrics, shadow diffing) is the delivery lane for
//! future server-side behaviors; the park module carries the whole game
//! today, so this module only has to instantiate cleanly and identify
//! its ABI generation.
#![no_std]

#[cfg(target_arch = "wasm32")]
#[panic_handler]
fn panic(_: &core::panic::PanicInfo) -> ! {
    core::arch::wasm32::unreachable()
}

/// The slot loader requires an exported memory.
static mut SCRATCH: [u8; 64] = [0; 64];

#[unsafe(no_mangle)]
pub extern "C" fn abi_version() -> u32 {
    unsafe { SCRATCH[0] = 2 };
    2
}

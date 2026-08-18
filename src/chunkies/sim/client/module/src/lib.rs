//! The client module: everything a replica host runs that isn't the park
//! itself — the session core (wire protocol, ordering, rollback, resync),
//! clock discipline, and frame smoothing. The host moves
//! bytes and owns platform verbs; every netcode decision is made in here,
//! so a browser, an app interpreter, and a load bot behave identically.
//!
//! ABI (all integers; fractional values Q16.16). Payloads move through the
//! staging buffer, which is the host's only window into this module.
//!
//! Session core (chunkies_session). There is one park — the journal
//! replica — and the renderer and HUD read it directly:
//!   abi_version() -> u32           the host refuses a version it does not
//!                                  know at boot; additions never bump it,
//!                                  removals and re-typings must
//!   session_buf() -> *mut u8, session_cap() -> u32
//!                                  inbound bytes and the ticket stage here
//!   session_init(my_dog u64, role u32, check_ms u32, nonce u32,
//!                now_ms u64)       role 0 spectator, 1 player; nonce is
//!                                  the high half of every intent id this
//!                                  page load mints
//!   session_connected(ticket_len u32, now_ms u64) -> u32
//!                                  ticket bytes are in the staging buffer;
//!                                  0 ok, 1 ticket too long
//!   session_disconnected()         the transport died
//!   session_on_stream(len u32, now_ms u64)     staged bytes, any framing
//!   session_on_datagram(len u32, now_ms u64)   one whole datagram
//!   session_pump(now_ms u64, budget_us u32) -> u32
//!                                  one frame of stepping, applying and
//!                                  checking; returns
//!                                  status bits: 1 have_state, 2 resyncing,
//!                                  4 stepped, 8 waiting on terrain,
//!                                  16 waiting on a module, 32 the world
//!                                  was corrected this pump — a restore, a
//!                                  repair, or a terrain change, which is
//!                                  every way it moves without a step — so
//!                                  absorb the discontinuity on THIS
//!                                  frame; clock state at bit 8.
//!                                  budget_us 0 observes without
//!                                  stepping — the clock still escalates
//!                                  and can still demand a snapshot, and
//!                                  events still apply, but no tick
//!                                  advances (the dev-loop freeze)
//!   session_reidentify(my_dog u64, role u32, nonce u32, now_ms u64)
//!                                  sign-in upgrade: new dog, role, and
//!                                  intent id space; pending intents drop,
//!                                  replica bookkeeping survives so the
//!                                  redial's hello keeps its since_seq
//!   session_set_visible(v u32)     hidden replicas send no checks
//!   session_terrain_ready(id u64, ok u32)      host loaded terrain `id`
//!   session_module_swapped(pw u32) host replaced the park module
//!   intent_join(now_ms u64) -> u64             every intent returns its id
//!   intent_check_in(now_ms u64) -> u64
//!   intent_move_to(node u32, now_ms u64) -> u64
//!   intent_boost(on u32, now_ms u64) -> u64
//!   session_diag(now_ms u64) -> u32
//!                                  writes the versioned diagnostics record
//!                                  (session crate: DIAG_VERSION/DIAG_BYTES)
//!                                  into the staging buffer and returns its
//!                                  length: tick, seq, clock, trail vs the
//!                                  1-tick target, counters — the ONE state
//!                                  read; a host parses, forwards, discards
//!
//! Imports (module `host`): park_apply(ptr, len) -> u32, park_step(),
//! park_snapshot(dst, cap) -> u32, park_restore(ptr, len) -> u32,
//! park_hash() -> u64, park_tick() -> u64,
//! send_stream(ptr, len), send_datagram(ptr, len),
//! inflate(src, slen, dst, cap) -> u32, request(kind, a),
//! emit(kind, a, b). Host requests: 1 need_terrain(id),
//! 2 need_module(pw), 3 resync_wanted(reason), 4 teardown(reason) — the
//! stream lost its framing, so the host must close the transport and
//! redial (a resync would ride the same broken stream, and QUIC keeps it
//! alive indefinitely). A module word (`pw`) is always the little-endian
//! load of the sha256 prefix's four wire bytes, never a re-formatting of
//! them.
//!
//! Presentation: the page writes one Q16.16 quad per dog into the frame
//! buffer, calls smooth_frame with its frame phase, and reads the presented
//! positions back from the first two slots of each quad. Interpolation and
//! corrections live exclusively on this side of the wire; nothing here ever
//! feeds back into world state.
//!   frame_cap() -> u32             max dogs per frame
//!   frame_buf() -> *mut i32        quads of [prev_x, prev_y, curr_x, curr_y]
//!   smooth_frame(n u32, alpha_q16 u32, snap_q16 u32)
//!                                  rewrites slots 0,1 of each quad in place
//!
//! Clock discipline belongs to the session — `session_pump` samples,
//! resets, and steps it. The render loop's only per-frame clock read:
//!   session_phase_q16() -> u32     Q16 frame phase (render alpha)
//! Everything else the clock knows rides the diagnostics record, and the
//! clock's state rides `session_pump`'s return at bit 8.
#![no_std]

use chunkies_session::{Host, Session};

#[cfg(target_arch = "wasm32")]
#[panic_handler]
fn panic(_: &core::panic::PanicInfo) -> ! {
    core::arch::wasm32::unreachable()
}

mod host {
    #[link(wasm_import_module = "host")]
    unsafe extern "C" {
        pub fn park_apply(ptr: u32, len: u32) -> u32;
        pub fn park_step();
        pub fn park_snapshot(dst: u32, cap: u32) -> u32;
        pub fn park_restore(ptr: u32, len: u32) -> u32;
        pub fn park_hash() -> u64;
        pub fn park_tick() -> u64;
        pub fn send_stream(ptr: u32, len: u32);
        pub fn send_datagram(ptr: u32, len: u32);
        pub fn inflate(src: u32, slen: u32, dst: u32, cap: u32) -> u32;
        pub fn request(kind: u32, a: u64);
        pub fn emit(kind: u32, a: u64, b: u64);
    }
}

const MAX_DOGS: usize = 2048;
const STAGE_CAP: usize = 64 * 1024;

static mut FRAME: [i32; MAX_DOGS * 4] = [0; MAX_DOGS * 4];
static mut SESSION: Session = Session::NEW;
static mut STAGE: [u8; STAGE_CAP] = [0; STAGE_CAP];

fn session() -> &'static mut Session {
    unsafe { &mut *(&raw mut SESSION) }
}

fn staged(len: u32) -> &'static [u8] {
    let len = (len as usize).min(STAGE_CAP);
    unsafe { core::slice::from_raw_parts(&raw const STAGE as *const u8, len) }
}

struct Wasm;

impl Host for Wasm {
    fn park_apply(&mut self, ev: &[u8]) -> u32 {
        unsafe { host::park_apply(ev.as_ptr() as u32, ev.len() as u32) }
    }

    fn park_step(&mut self) {
        unsafe { host::park_step() }
    }

    fn park_snapshot(&mut self, dst: &mut [u8]) -> u32 {
        unsafe { host::park_snapshot(dst.as_mut_ptr() as u32, dst.len() as u32) }
    }

    fn park_restore(&mut self, state: &[u8]) -> u32 {
        unsafe { host::park_restore(state.as_ptr() as u32, state.len() as u32) }
    }

    fn park_hash(&mut self) -> u64 {
        unsafe { host::park_hash() }
    }

    fn park_tick(&mut self) -> u64 {
        unsafe { host::park_tick() }
    }

    fn send_stream(&mut self, frame: &[u8]) {
        unsafe { host::send_stream(frame.as_ptr() as u32, frame.len() as u32) }
    }

    fn send_datagram(&mut self, datagram: &[u8]) {
        unsafe { host::send_datagram(datagram.as_ptr() as u32, datagram.len() as u32) }
    }

    fn inflate(&mut self, src: &[u8], dst: &mut [u8]) -> u32 {
        unsafe {
            host::inflate(
                src.as_ptr() as u32,
                src.len() as u32,
                dst.as_mut_ptr() as u32,
                dst.len() as u32,
            )
        }
    }

    fn request(&mut self, kind: u32, a: u64) {
        unsafe { host::request(kind, a) }
    }

    fn emit(&mut self, kind: u32, a: u64, b: u64) {
        unsafe { host::emit(kind, a, b) }
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn abi_version() -> u32 {
    // Generation 2: the v5 wire era. The replica pair and this session
    // module flip together; a host holding the other generation refuses
    // the boot rather than discovering the skew per frame.
    2
}

#[unsafe(no_mangle)]
pub extern "C" fn session_buf() -> *mut u8 {
    &raw mut STAGE as *mut u8
}

#[unsafe(no_mangle)]
pub extern "C" fn session_cap() -> u32 {
    STAGE_CAP as u32
}

#[unsafe(no_mangle)]
pub extern "C" fn session_init(my_dog: u64, role: u32, check_ms: u32, nonce: u32, now_ms: u64) {
    session().init(my_dog, role, check_ms, nonce, now_ms);
}

#[unsafe(no_mangle)]
pub extern "C" fn session_connected(ticket_len: u32, now_ms: u64) -> u32 {
    session().connected(&mut Wasm, staged(ticket_len), now_ms)
}

#[unsafe(no_mangle)]
pub extern "C" fn session_disconnected() {
    session().disconnected();
}

#[unsafe(no_mangle)]
pub extern "C" fn session_on_stream(len: u32, now_ms: u64) {
    session().on_stream(&mut Wasm, staged(len), now_ms);
}

#[unsafe(no_mangle)]
pub extern "C" fn session_on_datagram(len: u32, now_ms: u64) {
    session().on_datagram(&mut Wasm, staged(len), now_ms);
}

#[unsafe(no_mangle)]
pub extern "C" fn session_pump(now_ms: u64, budget_us: u32) -> u32 {
    session().pump(&mut Wasm, now_ms, budget_us)
}

#[unsafe(no_mangle)]
pub extern "C" fn session_reidentify(my_dog: u64, role: u32, nonce: u32, now_ms: u64) {
    session().reidentify(&mut Wasm, my_dog, role, nonce, now_ms);
}

#[unsafe(no_mangle)]
pub extern "C" fn session_set_visible(v: u32) {
    session().set_visible(v != 0);
}

#[unsafe(no_mangle)]
pub extern "C" fn session_terrain_ready(id: u64, ok: u32) {
    session().terrain_ready(&mut Wasm, id, ok != 0);
}

#[unsafe(no_mangle)]
pub extern "C" fn session_module_swapped(pw: u32) {
    session().module_swapped(&mut Wasm, pw);
}

#[unsafe(no_mangle)]
pub extern "C" fn intent_join(now_ms: u64) -> u64 {
    session().intent_join(&mut Wasm, now_ms)
}

#[unsafe(no_mangle)]
pub extern "C" fn intent_check_in(now_ms: u64) -> u64 {
    session().intent_check_in(&mut Wasm, now_ms)
}

#[unsafe(no_mangle)]
pub extern "C" fn intent_move_to(node: u32, now_ms: u64) -> u64 {
    session().intent_move_to(&mut Wasm, node, now_ms)
}

#[unsafe(no_mangle)]
pub extern "C" fn intent_boost(on: u32, now_ms: u64) -> u64 {
    session().intent_boost(&mut Wasm, on != 0, now_ms)
}

#[unsafe(no_mangle)]
pub extern "C" fn session_phase_q16() -> u32 {
    session().phase_q16()
}

#[unsafe(no_mangle)]
pub extern "C" fn session_diag(now_ms: u64) -> u32 {
    let stage = unsafe { core::slice::from_raw_parts_mut(&raw mut STAGE as *mut u8, STAGE_CAP) };
    session().diag(now_ms, stage) as u32
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
            chunkies_render::smooth(quad[0], quad[1], quad[2], quad[3], alpha_q16, snap_q16);
        quad[0] = sx;
        quad[1] = sy;
    }
}

//! chunkies-abi: the contract between a game simulation and every host
//! that runs it. A game implements [`Simulation`] on its state type and
//! invokes [`export_simulation!`] once in its module crate; the macro
//! emits the entire wasm export surface — statics, buffer plumbing, the
//! SimEvent envelope split, `abi_version` — so a game crate contains no
//! `unsafe`, no `extern "C"`, and no way to misdeclare the ABI.
//!
//! ABI generation 2. The v2 event encoding is the SimEvent:
//! `kind u16 | actor u64 | payload`, little-endian — the exact trailing
//! bytes of the wire protocol's EventRecord, so the authority hands a
//! received record's slice to `sim_apply` without re-framing. The macro
//! performs the split; [`Simulation::apply`] receives typed fields and
//! can misparse nothing.
//!
//! Versioning doctrine (stated once, here): additions never bump
//! `ABI_VERSION`; removals and re-typings must. A new trait method ships
//! with a default implementation returning [`REJECT_UNSUPPORTED`] so
//! every rebuilt game exports it, and hosts treat new export names as
//! probe-optional until the module fleet has converged.
//!
//! Content: games with large immutable content (identified by a u64
//! content address, delivered out-of-band by the host) implement
//! [`Simulation::content_stage`] and pass a nonzero `content_cap` to the
//! macro. Games without opt out with `content_cap = 0` — the host skips
//! the fetch dance entirely. What the bytes mean is the game's business;
//! the framework only moves and addresses them.

#![cfg_attr(not(test), no_std)]

/// The ABI generation the macro stamps into `abi_version()`.
pub const ABI_VERSION: u32 = 2;

/// SimEvent prefix: kind u16, actor u64, then the game payload.
pub const SIM_EVENT_HEADER: usize = 10;

/// Accepted.
pub const OK: u32 = 0;

/// Framework reject codes live at the top of the u32 space; game codes
/// stay below [`REJECT_FRAMEWORK_BASE`] and the two can never collide.
pub const REJECT_FRAMEWORK_BASE: u32 = 0xFFFF_0000;
/// The event's SimEvent envelope is malformed (short, or over the cap).
pub const REJECT_ENVELOPE: u32 = 0xFFFF_0001;
/// The verb exists in the ABI but this game does not implement it.
pub const REJECT_UNSUPPORTED: u32 = 0xFFFF_0002;

/// One bounded, deterministic game simulation. Everything is
/// single-threaded and host-driven: the host calls exactly one export at
/// a time and never reenters — the macro's statics lean on that contract.
///
/// Determinism obligations (enforced empirically by gametest, not by the
/// compiler): `apply` must reject without mutating on any nonzero return;
/// `hash` must cover every bit of state that can influence evolution;
/// `snapshot` must be canonical (restore then re-snapshot is
/// byte-identical) and complete (a restored instance evolves identically
/// forever).
pub trait Simulation {
    /// The pre-`init` state. Wasm statics cannot run constructors, so
    /// the macro's instance is born from this constant.
    const NEW: Self;

    /// Resets to genesis for a chunk. Nonzero = the module cannot open
    /// (e.g. content required but not staged).
    fn init(&mut self, seed: u64, chunk: u64, epoch: u32) -> u32;

    /// Applies one event. Zero accepts; any nonzero code must leave
    /// state untouched — reads are fine, mutation is not.
    fn apply(&mut self, kind: u16, actor: u64, payload: &[u8]) -> u32;

    /// Advances one tick. Pure function of state.
    fn step(&mut self);

    /// The canonical state hash.
    fn hash(&self) -> u64;

    /// Writes the canonical snapshot into `dst`, returning its length;
    /// 0 means it does not fit (size `dst` so it always does).
    fn snapshot(&self, dst: &mut [u8]) -> usize;

    /// Adopts a snapshot. Nonzero = rejected, state untouched.
    fn restore(&mut self, src: &[u8]) -> u32;

    fn tick(&self) -> u64;
    fn epoch(&self) -> u32;

    /// The current rate segment of the piecewise tick<->wall mapping:
    /// tick `anchor_tick` sits `anchor_ns` after the wall epoch and later
    /// ticks advance at `rate` Hz. State, not host config, so every
    /// authority generation and every replay derives the same schedule.
    fn rate(&self) -> u32;
    fn anchor_tick(&self) -> u64;
    fn anchor_ns(&self) -> u64;

    /// Adopts staged content bytes. The slice aliases the macro's content
    /// buffer and is valid only until the next stage call — the one
    /// lending contract in this ABI. Games without content keep the
    /// default.
    fn content_stage(&mut self, _blob: &'static [u8]) -> u32 {
        REJECT_UNSUPPORTED
    }

    /// The content address of what is adopted; 0 when content-free.
    fn content_id(&self) -> u64 {
        0
    }
}

/// Emits the wasm export surface for one [`Simulation`] type.
///
/// ```ignore
/// chunkies_abi::export_simulation!(type = Toy, io_cap = 65536, content_cap = 0);
/// ```
///
/// Also generated: `with_state`, the safe accessor a game's extension
/// exports (projections) reach their state and the io buffer through, so
/// hand-authored exports need no `unsafe` either.
#[macro_export]
macro_rules! export_simulation {
    (type = $ty:ty, io_cap = $io_cap:expr, content_cap = $content_cap:expr) => {
        static mut __CHUNKIES_STATE: $ty = <$ty as $crate::Simulation>::NEW;
        static mut __CHUNKIES_IO: [u8; $io_cap] = [0; $io_cap];
        static mut __CHUNKIES_CONTENT: [u8; $content_cap] = [0; $content_cap];

        /// Single-threaded access to the simulation and the io buffer,
        /// for game extension exports. Sound because the host calls one
        /// export at a time and never reenters.
        pub fn with_state<R>(f: impl FnOnce(&mut $ty, &mut [u8]) -> R) -> R {
            unsafe {
                f(
                    &mut *(&raw mut __CHUNKIES_STATE),
                    &mut *(&raw mut __CHUNKIES_IO),
                )
            }
        }

        #[unsafe(no_mangle)]
        pub extern "C" fn abi_version() -> u32 {
            $crate::ABI_VERSION
        }

        #[unsafe(no_mangle)]
        pub extern "C" fn io_buf() -> *mut u8 {
            &raw mut __CHUNKIES_IO as *mut u8
        }

        #[unsafe(no_mangle)]
        pub extern "C" fn io_cap() -> u32 {
            $io_cap as u32
        }

        #[unsafe(no_mangle)]
        pub extern "C" fn content_buf() -> *mut u8 {
            &raw mut __CHUNKIES_CONTENT as *mut u8
        }

        #[unsafe(no_mangle)]
        pub extern "C" fn content_cap() -> u32 {
            $content_cap as u32
        }

        /// Parses and adopts the blob the host wrote into the content
        /// buffer. Staging invalidates whatever was adopted before, so on
        /// failure no content is loaded.
        #[unsafe(no_mangle)]
        pub extern "C" fn sim_content_stage(len: u32) -> u32 {
            let len = len as usize;
            if len > $content_cap {
                return $crate::REJECT_ENVELOPE;
            }
            let blob: &'static [u8] = unsafe {
                core::slice::from_raw_parts(&raw const __CHUNKIES_CONTENT as *const u8, len)
            };
            with_state(|s, _| $crate::Simulation::content_stage(s, blob))
        }

        /// Copies bytes into the content buffer and adopts them — the
        /// same path a wasm host drives through content_buf +
        /// sim_content_stage, callable safely from in-crate tests.
        pub fn stage_content(bytes: &[u8]) -> u32 {
            if bytes.len() > $content_cap {
                return $crate::REJECT_ENVELOPE;
            }
            unsafe {
                (&mut *(&raw mut __CHUNKIES_CONTENT))[..bytes.len()].copy_from_slice(bytes);
            }
            sim_content_stage(bytes.len() as u32)
        }

        #[unsafe(no_mangle)]
        pub extern "C" fn sim_content_id() -> u64 {
            with_state(|s, _| $crate::Simulation::content_id(s))
        }

        #[unsafe(no_mangle)]
        pub extern "C" fn sim_init(seed: u64, chunk: u64, epoch: u32) -> u32 {
            with_state(|s, _| $crate::Simulation::init(s, seed, chunk, epoch))
        }

        /// The SimEvent envelope split lives here, once: the game's
        /// `apply` receives typed fields and cannot misparse the prefix.
        #[unsafe(no_mangle)]
        pub extern "C" fn sim_apply(len: u32) -> u32 {
            let len = len as usize;
            if len > $io_cap || len < $crate::SIM_EVENT_HEADER {
                return $crate::REJECT_ENVELOPE;
            }
            unsafe {
                let io = &*(&raw const __CHUNKIES_IO);
                let kind = u16::from_le_bytes([io[0], io[1]]);
                let actor =
                    u64::from_le_bytes([io[2], io[3], io[4], io[5], io[6], io[7], io[8], io[9]]);
                let payload = &io[$crate::SIM_EVENT_HEADER..len];
                $crate::Simulation::apply(&mut *(&raw mut __CHUNKIES_STATE), kind, actor, payload)
            }
        }

        #[unsafe(no_mangle)]
        pub extern "C" fn sim_step() {
            with_state(|s, _| $crate::Simulation::step(s))
        }

        #[unsafe(no_mangle)]
        pub extern "C" fn sim_hash() -> u64 {
            with_state(|s, _| $crate::Simulation::hash(s))
        }

        #[unsafe(no_mangle)]
        pub extern "C" fn sim_snapshot() -> u32 {
            with_state(|s, io| $crate::Simulation::snapshot(s, io) as u32)
        }

        #[unsafe(no_mangle)]
        pub extern "C" fn sim_restore(len: u32) -> u32 {
            let len = len as usize;
            if len > $io_cap {
                return $crate::REJECT_ENVELOPE;
            }
            unsafe {
                let io = &*(&raw const __CHUNKIES_IO);
                $crate::Simulation::restore(&mut *(&raw mut __CHUNKIES_STATE), &io[..len])
            }
        }

        #[unsafe(no_mangle)]
        pub extern "C" fn sim_tick() -> u64 {
            with_state(|s, _| $crate::Simulation::tick(s))
        }

        #[unsafe(no_mangle)]
        pub extern "C" fn sim_epoch() -> u32 {
            with_state(|s, _| $crate::Simulation::epoch(s))
        }

        #[unsafe(no_mangle)]
        pub extern "C" fn sim_rate() -> u32 {
            with_state(|s, _| $crate::Simulation::rate(s))
        }

        #[unsafe(no_mangle)]
        pub extern "C" fn sim_anchor_tick() -> u64 {
            with_state(|s, _| $crate::Simulation::anchor_tick(s))
        }

        #[unsafe(no_mangle)]
        pub extern "C" fn sim_anchor_ns() -> u64 {
            with_state(|s, _| $crate::Simulation::anchor_ns(s))
        }

        /// The module's only panic path: an abort the host observes as a
        /// trap. Native builds (tests, tools) use std's handler instead.
        #[cfg(target_arch = "wasm32")]
        #[panic_handler]
        fn __chunkies_panic(_: &core::panic::PanicInfo) -> ! {
            core::arch::wasm32::unreachable()
        }
    };
}

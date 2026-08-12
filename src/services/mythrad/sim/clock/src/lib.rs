//! Clock discipline for replica hosts: the state machine that keeps a
//! client simulation phase-locked to the authority's tick, re-acquires
//! after stalls, and knows when stepping is the wrong tool and a
//! snapshot is cheaper. Pure and allocation-free: time is an input, the
//! host executes directives — so every property here is provable in
//! simulated time, and every host (browser, app interpreter, load bot)
//! inherits the same behavior.
//!
//! States and their bounded exits:
//!   Acquiring         no server sample yet; step nothing.
//!   Locked            |error| small: 0..=4 steps/frame from a fractional
//!                     accumulator, slewed by a proportional controller
//!                     (clamped ±2%, ±¼-tick deadband) — smooth, no jumps.
//!   FastForward       behind by > 12 ticks: step as hard as the host's
//!                     per-frame compute budget allows until within 2.
//!   SnapshotRequired  behind by > 30s (the server hash ring: past it,
//!                     stepping wastes battery and checks can't verify)
//!                     or AHEAD by > 2s (never step backward): ask the
//!                     host to resync; only clock_reset leaves this state.
//!
//! The server-tick model is a reference point (ref_ms, ref_tick) advanced
//! by the local monotonic clock and corrected by verdict samples through
//! an EWMA — an offset filter, not a jump, so one delayed verdict cannot
//! yank the target.
//!
//! Fixed point throughout (Q16.16 ticks); no floats anywhere.
#![cfg_attr(not(test), no_std)]

const ONE: i64 = 1 << 16;

pub const TICK_HZ: u64 = 24;
/// The replica deliberately trails server-now (events in flight arrive
/// before their tick is stepped).
pub const LAG_TICKS: i64 = 6;

const ENTER_FF_Q16: i64 = 12 * ONE;
const EXIT_FF_Q16: i64 = 2 * ONE;
const SNAPSHOT_BEHIND_Q16: i64 = 720 * ONE; // the 30s server hash ring
const SNAPSHOT_AHEAD_Q16: i64 = 48 * ONE;
const DEADBAND_Q16: i64 = ONE / 4;
const SLEW_MAX_Q16: i64 = (2 * ONE) / 100; // ±2% tick-rate adjustment
/// err/SLEW_DIV = adjustment: reaches the ±2% clamp near the FF border.
const SLEW_DIV: i64 = 64;
/// Advisory pacing for FastForward: how much host budget one step costs.
const STEP_COST_US: u32 = 30;
const LOCKED_MAX_STEPS: u32 = 4;
const FF_MAX_STEPS: u32 = 4096;
/// Re-ask for a snapshot while stuck (the first request can race a
/// transport death).
const REREQUEST_MS: u64 = 2000;

/// Directive flag: the host should request a snapshot resync.
pub const F_SNAPSHOT: u32 = 1 << 16;

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum State {
    Acquiring,
    Locked,
    FastForward,
    SnapshotRequired,
}

pub struct Clock {
    state: State,
    ref_ms: u64,
    ref_tick_q16: i64, // server tick (Q16) at ref_ms
    rtt_ms_q16: i64,
    frac_q16: i64, // fractional step accumulator (Locked)
    last_frame_ms: u64,
    last_req_ms: u64,
}

fn ticks_q16(ms: u64) -> i64 {
    ((ms as i64) * (TICK_HZ as i64) * ONE) / 1000
}

impl Clock {
    pub const NEW: Clock = Clock {
        state: State::Acquiring,
        ref_ms: 0,
        ref_tick_q16: 0,
        rtt_ms_q16: 0,
        frac_q16: 0,
        last_frame_ms: 0,
        last_req_ms: 0,
    };

    pub fn state(&self) -> State {
        self.state
    }

    pub fn rtt_ms_q16(&self) -> i64 {
        self.rtt_ms_q16
    }

    /// One server time sample: a verdict echoed back with the tick the
    /// authority reported. `send_ms`/`recv_ms` are the host's clock at
    /// datagram send and receive.
    pub fn sample(&mut self, send_ms: u64, recv_ms: u64, server_tick: u64) {
        let rtt = (recv_ms.saturating_sub(send_ms)).clamp(1, 10_000) as i64;
        let measured = (server_tick as i64) * ONE + ticks_q16((rtt / 2) as u64);
        if self.state == State::Acquiring {
            self.rtt_ms_q16 = rtt * ONE;
            self.ref_ms = recv_ms;
            self.ref_tick_q16 = measured;
            self.state = State::Locked;
            return;
        }
        self.rtt_ms_q16 += (rtt * ONE - self.rtt_ms_q16) / 5; // EWMA 0.8/0.2
        // Offset filter: blend the measurement into the model's prediction
        // at recv time (1/8 gain), then re-anchor the reference there.
        let predicted = self.ref_tick_q16 + ticks_q16(recv_ms.saturating_sub(self.ref_ms));
        let blended = predicted + (measured - predicted) / 8;
        self.ref_ms = recv_ms;
        self.ref_tick_q16 = blended;
    }

    /// Re-seed after a snapshot restore or module swap: the replica now
    /// sits at `server_tick`, observed at `now_ms`.
    pub fn reset(&mut self, server_tick: u64, now_ms: u64) {
        self.ref_ms = now_ms;
        self.ref_tick_q16 = (server_tick as i64) * ONE;
        self.frac_q16 = 0;
        self.state = State::Locked;
    }

    fn target_q16(&self, now_ms: u64) -> i64 {
        self.ref_tick_q16 + ticks_q16(now_ms.saturating_sub(self.ref_ms)) - LAG_TICKS * ONE
    }

    /// Once per frame: returns steps to run now (low 16 bits) and the
    /// F_SNAPSHOT flag. The host may execute fewer steps than directed
    /// (it reports reality through `replica_tick` next frame).
    pub fn frame(&mut self, now_ms: u64, replica_tick: u64, budget_us: u32) -> u32 {
        if self.state == State::Acquiring {
            return 0;
        }
        let dt_ms = now_ms.saturating_sub(self.last_frame_ms).min(250);
        self.last_frame_ms = now_ms;
        let err_q16 = self.target_q16(now_ms) - (replica_tick as i64) * ONE;

        let was = self.state;
        if err_q16 > SNAPSHOT_BEHIND_Q16 || err_q16 < -SNAPSHOT_AHEAD_Q16 {
            self.state = State::SnapshotRequired;
        }
        match self.state {
            State::SnapshotRequired => {
                // ask immediately on entry, then re-ask while stuck (the
                // first request can race a dying transport)
                if was != State::SnapshotRequired
                    || now_ms.saturating_sub(self.last_req_ms) >= REREQUEST_MS
                {
                    self.last_req_ms = now_ms;
                    return F_SNAPSHOT;
                }
                0
            }
            State::Locked | State::FastForward => {
                if self.state == State::Locked && err_q16 > ENTER_FF_Q16 {
                    self.state = State::FastForward;
                }
                if self.state == State::FastForward {
                    if err_q16 > EXIT_FF_Q16 {
                        // ceil, not floor: an error sitting just past the
                        // exit band must still make progress every frame
                        let deficit = (((err_q16 - EXIT_FF_Q16) >> 16) as u32) + 1;
                        return deficit.min(budget_us / STEP_COST_US).min(FF_MAX_STEPS);
                    }
                    self.state = State::Locked;
                    self.frac_q16 = 0;
                }
                let adj = if err_q16.abs() < DEADBAND_Q16 {
                    0
                } else {
                    (err_q16 / SLEW_DIV).clamp(-SLEW_MAX_Q16, SLEW_MAX_Q16)
                };
                self.frac_q16 += (ticks_q16(dt_ms) * (ONE + adj)) >> 16;
                if self.frac_q16 < 0 {
                    self.frac_q16 = 0;
                }
                let steps = ((self.frac_q16 >> 16) as u32).min(LOCKED_MAX_STEPS);
                self.frac_q16 -= (steps as i64) * ONE;
                if self.frac_q16 > ONE {
                    self.frac_q16 = ONE; // dt spike: no windup past one tick
                }
                steps
            }
            State::Acquiring => 0,
        }
    }

    /// Frame phase within the current tick, Q16 in [0, ONE): the render
    /// interpolation alpha.
    pub fn phase_q16(&self) -> u32 {
        (self.frac_q16.clamp(0, ONE - 1)) as u32
    }

    /// The current phase error in Q16 ticks (positive = replica behind
    /// target). Pure read for diagnostics; no state change.
    pub fn error_q16(&self, now_ms: u64, replica_tick: u64) -> i64 {
        if self.state == State::Acquiring {
            return 0;
        }
        self.target_q16(now_ms) - (replica_tick as i64) * ONE
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // A simulated host: 60fps frames, faithful step execution, verdict
    // samples every `check_ms` with a fixed rtt. Server ticks advance in
    // real time. Returns (clock, replica_tick, sim_now).
    struct Harness {
        clock: Clock,
        now_ms: u64,
        replica_tick: u64,
        server_tick0: u64, // server tick at now=0
        rtt_ms: u64,
        check_ms: u64,
        last_check: u64,
        snapshots: u32,
    }

    impl Harness {
        fn new(deficit_ticks: u64, rtt_ms: u64) -> Harness {
            let server_tick0 = 10_000;
            let mut h = Harness {
                clock: Clock::NEW,
                now_ms: 0,
                replica_tick: server_tick0 - LAG_TICKS as u64 - deficit_ticks,
                server_tick0,
                rtt_ms,
                check_ms: 5000,
                last_check: 0,
                snapshots: 0,
            };
            // welcome sample seeds the model
            h.clock
                .sample(0, h.rtt_ms, h.server_tick(0).saturating_sub(0));
            h
        }

        fn server_tick(&self, at_ms: u64) -> u64 {
            self.server_tick0 + at_ms * TICK_HZ / 1000
        }

        // run one 60fps frame with an 8ms step budget
        fn step_frame(&mut self) {
            self.now_ms += 16;
            if self.now_ms - self.last_check >= self.check_ms {
                self.last_check = self.now_ms;
                self.clock
                    .sample(self.now_ms - self.rtt_ms, self.now_ms, self.server_tick(self.now_ms));
            }
            let d = self.clock.frame(self.now_ms, self.replica_tick, 8_000);
            if d & F_SNAPSHOT != 0 {
                self.snapshots += 1;
                // host resyncs: restore at server tick minus lag
                self.replica_tick = self.server_tick(self.now_ms) - LAG_TICKS as u64;
                self.clock.reset(self.replica_tick, self.now_ms);
                return;
            }
            self.replica_tick += (d & 0xFFFF) as u64;
        }

        fn err_ticks(&self) -> i64 {
            self.server_tick(self.now_ms) as i64 - LAG_TICKS - self.replica_tick as i64
        }

        fn run_ms(&mut self, ms: u64) {
            for _ in 0..ms / 16 {
                self.step_frame();
            }
        }
    }

    #[test]
    fn locked_holds_within_a_tick_at_steady_state() {
        let mut h = Harness::new(0, 100);
        h.run_ms(30_000);
        assert_eq!(h.clock.state(), State::Locked);
        assert!(h.err_ticks().abs() <= 1, "err={} ticks", h.err_ticks());
        assert_eq!(h.snapshots, 0);
    }

    #[test]
    fn deficits_recover_in_bounded_wall_time() {
        // 7.5s behind (the measured Firefox pathology): must re-lock in
        // under 2s of wall time, not the ~6 minutes the old slew took.
        for deficit in [30u64, 180, 600] {
            let mut h = Harness::new(deficit, 100);
            h.run_ms(2_000);
            assert_eq!(h.clock.state(), State::Locked, "deficit {deficit}");
            assert!(
                h.err_ticks().abs() <= 2,
                "deficit {deficit}: err={} after 2s",
                h.err_ticks()
            );
            assert_eq!(h.snapshots, 0, "deficit {deficit} must not snapshot");
        }
    }

    #[test]
    fn one_excursion_one_transition() {
        let mut h = Harness::new(200, 100);
        let mut transitions = 0;
        let mut prev = h.clock.state();
        for _ in 0..(10_000 / 16) {
            h.step_frame();
            let s = h.clock.state();
            if s != prev {
                transitions += 1;
                prev = s;
            }
        }
        // Locked -> FastForward -> Locked: one round trip, nothing more.
        assert_eq!(transitions, 2, "state flapped");
        assert_eq!(h.clock.state(), State::Locked);
    }

    #[test]
    fn beyond_the_ring_snapshots_instead_of_stepping() {
        let mut h = Harness::new(1000, 100); // ~42s behind
        h.run_ms(1_000);
        assert!(h.snapshots >= 1, "must resync, not step through 42s");
        assert_eq!(h.clock.state(), State::Locked);
        assert!(h.err_ticks().abs() <= 2);
    }

    #[test]
    fn ahead_never_steps_backward_and_resyncs_when_gross() {
        // slightly ahead: hold (steps 0) until the server catches up
        let mut h = Harness::new(0, 100);
        h.replica_tick += 20;
        let before = h.replica_tick;
        h.run_ms(320);
        assert!(h.replica_tick >= before, "stepped backward");
        // grossly ahead (broken local clock / resume weirdness): resync
        let mut g = Harness::new(0, 100);
        g.replica_tick += 200;
        g.run_ms(2_000);
        assert!(g.snapshots >= 1);
        assert_eq!(g.clock.state(), State::Locked);
    }

    #[test]
    fn slew_stays_inside_two_percent_in_locked() {
        let mut h = Harness::new(8, 100); // inside Locked band
        let start_tick = h.replica_tick;
        let start_ms = h.now_ms;
        h.run_ms(5_000);
        let stepped = (h.replica_tick - start_tick) as i64;
        let nominal = ((h.now_ms - start_ms) * TICK_HZ / 1000) as i64;
        let excess = stepped - nominal;
        // 5s at +2% is ~2.4 extra ticks; the initial 8-tick error plus
        // slack bounds the total overshoot
        assert!(excess <= 11, "slew exceeded clamp: +{excess} ticks");
        assert_eq!(h.snapshots, 0);
    }

    #[test]
    fn rtt_jitter_does_not_break_the_lock() {
        let mut h = Harness::new(0, 100);
        // jittery verdicts: rtt swings 40..400ms deterministically
        for i in 0..(60_000u64 / 16) {
            h.rtt_ms = 40 + (i * 37) % 360;
            h.step_frame();
        }
        assert_eq!(h.clock.state(), State::Locked);
        assert!(h.err_ticks().abs() <= 2, "err={}", h.err_ticks());
        assert_eq!(h.snapshots, 0);
    }
}

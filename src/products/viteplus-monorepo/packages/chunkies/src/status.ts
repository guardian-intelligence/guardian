// The host's two status vocabularies, decoded once: the connection-level
// `HostState` a UI subscribes to, and the per-pump `PumpStatus` the frame
// loop reads. The raw pump word and its bit layout stay private to this
// file — a consumer never sees a flag it has to mask.

import type { RoleName } from "./ids.ts";

const CLOCK_STATES = ["acquiring", "locked", "fastForward", "snapshotRequired"] as const;

/** Clock states by the sim/clock crate's numbering, carried as names. */
export type ClockState = (typeof CLOCK_STATES)[number];

export type HostPhase =
  | "idle"
  | "booting"
  | "connecting"
  | "live"
  | "resyncing"
  | "unreachable"
  | "dead";

/** Everything the host can tell a subscriber about the session. */
export type HostState = {
  readonly phase: HostPhase;
  /** Straight failed dials since the last success. */
  readonly dialAttempts: number;
  readonly epoch: number;
  /** The running replica module, as its display word spells it. */
  readonly replicaModuleWord: string;
  /** The tick rate from the welcome, or null before one arrives. */
  readonly rateHz: number | null;
  /**
   * The granted role, which may be narrower than the one asked for.
   * Requested at boot, overwritten by the welcome; null before boot.
   */
  readonly role: RoleName | null;
};

/** One `session_pump`, decoded. */
export type PumpStatus = {
  /** A world is live: the replica has state and is stepping it. */
  readonly haveState: boolean;
  /** This pump ran at least one sim step. */
  readonly stepped: boolean;
  /** A resync is outstanding; the core is waiting on a snapshot. */
  readonly resyncing: boolean;
  /** What a held snapshot is waiting on, if anything. */
  readonly waiting: "blob" | "module" | null;
  /**
   * The world was corrected on this pump — a snapshot restored it, or a
   * repair rewound and replayed it — so entities may have moved without
   * time passing. A presenter owes the screen a glide from where it last
   * showed them, on THIS frame: a frame later is a frame too late,
   * because by then the jump has been drawn.
   */
  readonly corrected: boolean;
  readonly clock: ClockState;
};

const PumpFlag = {
  haveState: 1,
  resyncing: 1 << 1,
  stepped: 1 << 2,
  waitingBlob: 1 << 3,
  waitingModule: 1 << 4,
  corrected: 1 << 5,
} as const;

const CLOCK_STATE_SHIFT = 8;

export function clockStateName(state: number): ClockState {
  return CLOCK_STATES[state & 3]!;
}

export function decodePumpStatus(word: number): PumpStatus {
  return {
    haveState: (word & PumpFlag.haveState) !== 0,
    stepped: (word & PumpFlag.stepped) !== 0,
    resyncing: (word & PumpFlag.resyncing) !== 0,
    waiting:
      (word & PumpFlag.waitingBlob) !== 0
        ? "blob"
        : (word & PumpFlag.waitingModule) !== 0
          ? "module"
          : null,
    corrected: (word & PumpFlag.corrected) !== 0,
    clock: clockStateName(word >> CLOCK_STATE_SHIFT),
  };
}

/**
 * Unpacks the `b` argument of telemetry code 2, `welcome(a = epoch,
 * b = hz | role << 32)`. The granted role rides here because it can be
 * narrower than the one the host asked for — a token that no longer
 * proves a player comes back as a spectator — and this is the only place
 * the session ABI reports it.
 */
export function decodeWelcomeEmit(b: bigint): { hz: number; role: RoleName } {
  return {
    hz: Number(BigInt.asUintN(32, b)),
    role: Number(b >> 32n) === 1 ? "player" : "spectator",
  };
}

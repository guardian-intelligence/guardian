import { clockStateName, type ClockState } from "./status.ts";

/** Bytes in the `session_diag` record. */
export const DIAG_BYTES = 96;

const DIAG_VERSION = 1;

/**
 * The session's diagnostics record, decoded. Ticks are whole numbers
 * with the Q16 fraction included; `trailTicks` is distance behind the
 * authority's present (what `trailTargetTicks` bounds — the module
 * states its own invariant, so no host mirrors the constant), while
 * `errorTicks` is distance from the cushioned schedule the clock
 * actually steers by, ~0 for a replica holding that schedule perfectly.
 */
export type Diag = {
  readonly clockState: ClockState;
  readonly rttMs: number;
  readonly trailTicks: number;
  readonly errorTicks: number;
  readonly tick: bigint;
  readonly seq: bigint;
  /** The invariant: trail should stay within this many ticks. */
  readonly trailTargetTicks: number;
  /** The operating cushion the clock currently steers to. */
  readonly cushionTicks: number;
  readonly events: number;
  readonly rollbacks: number;
  readonly resyncs: number;
  readonly checks: number;
  readonly mismatches: number;
  readonly rejects: number;
};

/** Reads a `session_diag` record. Null when the module wrote a version we don't know. */
export function decodeDiag(bytes: Uint8Array): Diag | null {
  if (bytes.length < DIAG_BYTES) return null;
  const dv = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  if (dv.getUint16(0, true) !== DIAG_VERSION) return null;
  return {
    clockState: clockStateName(dv.getUint8(2)),
    rttMs: dv.getUint32(4, true),
    trailTicks: Number(dv.getBigInt64(8, true)) / 65536,
    errorTicks: Number(dv.getBigInt64(16, true)) / 65536,
    tick: dv.getBigUint64(24, true),
    seq: dv.getBigInt64(32, true),
    trailTargetTicks: dv.getUint32(40, true),
    cushionTicks: dv.getUint32(44, true),
    events: Number(dv.getBigUint64(48, true)),
    rollbacks: Number(dv.getBigUint64(56, true)),
    resyncs: Number(dv.getBigUint64(64, true)),
    checks: Number(dv.getBigUint64(72, true)),
    mismatches: Number(dv.getBigUint64(80, true)),
    rejects: Number(dv.getBigUint64(88, true)),
  };
}

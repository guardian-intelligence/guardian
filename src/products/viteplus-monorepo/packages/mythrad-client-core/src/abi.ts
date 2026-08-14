// The two wasm ABIs the host binds together, as TypeScript. `park.wasm`
// is the simulation; `client.wasm` carries the session core, the clock,
// and the presentation smoother. Neither module imports the other — the
// host is the only thing that can see both linear memories, and copying
// between them is its whole job on the park verbs.
//
// There is one world, and the park verbs address it directly: what the
// session applies is what the renderer reads.
//
// wasm i64 crosses the boundary as bigint, so every u64/i64 here is a
// bigint and every u32/i32 a number. Getting that wrong is a silent
// truncation, not a type error, at the wasm boundary.

/** `park.wasm`'s exports, as the host uses them. */
export interface ParkExports {
  readonly memory: WebAssembly.Memory;
  io_buf(): number;
  io_cap(): number;
  terrain_buf(): number;
  terrain_cap(): number;
  /** Adopts the blob already written into `terrain_buf`. 0 on success. */
  sim_set_terrain(len: number): number;
  /**
   * Creates a fresh world on the loaded terrain. The host never calls
   * this — a client's world only ever arrives as a snapshot — but it is
   * part of the module's ABI and is what a test authority boots with.
   */
  sim_init(seed: bigint, parkId: bigint, epoch: number): number;
  /** The id of the terrain currently loaded, as the wire and `/terrain/<hex>` spell it. */
  sim_terrain_id(): bigint;
  /** Applies the event bytes in `io_buf` (leading kind u16). 0 on success. */
  sim_apply(len: number): number;
  sim_step(): void;
  /** Writes the MYP3 state into `io_buf`; returns its length. */
  sim_snapshot(): number;
  /** Restores from `io_buf`. 0 on success; 4 means the terrain does not match. */
  sim_restore(len: number): number;
  sim_hash(): bigint;
  sim_tick(): bigint;
  /** Writes the presentation projection into `io_buf`; returns its length. */
  sim_view(): number;
  /** Writes the 28-byte HUD record for `selfId` into `io_buf`; returns its length. */
  sim_hud(selfId: bigint): number;
}

/** `client.wasm`'s exports: the session core, plus the clock and smoother it links. */
export interface ClientExports {
  readonly memory: WebAssembly.Memory;

  /** Host staging area for inbound bytes and the outbound ticket. */
  session_buf(): number;
  session_cap(): number;

  session_init(myDog: bigint, role: number, checkMs: number, nonce: number, nowMs: bigint): void;
  /**
   * Adopts a new identity without discarding the replica: state and
   * `since_seq` survive, pending intents are dropped, and the fresh nonce
   * starts a new intent-id space so nothing from the old identity can be
   * mistaken for an ack.
   */
  session_reidentify(myDog: bigint, role: number, nonce: number, nowMs: bigint): void;
  /** `len` bytes of stream were staged in `session_buf`; append them to the decoder. */
  session_on_stream(len: number, nowMs: bigint): void;
  /** One whole datagram was staged in `session_buf`. */
  session_on_datagram(len: number, nowMs: bigint): void;
  /**
   * The ticket was staged in `session_buf`; build and send the hello
   * frame. Returns non-zero when the ticket does not fit the outbound
   * buffer. A player role also sends its own join intent from here — the
   * host must not send one.
   */
  session_connected(ticketLen: number, nowMs: bigint): number;
  /**
   * The transport died. Required on every teardown: without it a queued
   * resync writes into a dead stream and the half-frame left in the
   * reassembly buffer corrupts the next connection's first read.
   */
  session_disconnected(): void;
  /** Drives the clock, steps the world, applies ready events. Returns status flags. */
  session_pump(nowMs: bigint, budgetUs: number): number;
  session_terrain_ready(id: bigint, ok: number): void;
  session_module_swapped(pw: number): void;
  /** Hidden pages stay silent: no check datagrams while `v` is 0. */
  session_set_visible(v: number): void;

  intent_join(nowMs: bigint): bigint;
  intent_check_in(nowMs: bigint): bigint;
  intent_move_to(node: number, nowMs: bigint): bigint;
  intent_boost(on: number, nowMs: bigint): bigint;

  /**
   * Q16 phase between ticks: the renderer's interpolation alpha. Read
   * from the session's own clock — the module's standalone `clock_*`
   * exports are gone precisely because they tracked a second Clock that
   * nothing disciplined.
   */
  session_phase_q16(): number;

  /**
   * Writes the versioned diagnostics record into `session_buf` and
   * returns its length. The ONE state read: tick, seq, clock, trail vs
   * its target, and every counter ride this record (`decodeDiag`), so a
   * new diagnostic is a field in the record, never a new export. The
   * host parses, forwards, and discards — raw dumps are never retained.
   */
  session_diag(nowMs: bigint): number;

  frame_cap(): number;
  frame_buf(): number;
  smooth_frame(n: number, alphaQ16: number, snapQ16: number): void;
}

/** The `host` import object the session core links against. */
export interface HostImports {
  park_apply(ptr: number, len: number): number;
  park_step(): void;
  park_snapshot(dst: number, cap: number): number;
  park_restore(ptr: number, len: number): number;
  park_hash(): bigint;
  park_tick(): bigint;
  send_stream(ptr: number, len: number): void;
  send_datagram(ptr: number, len: number): void;
  inflate(src: number, slen: number, dst: number, cap: number): number;
  request(kind: number, a: bigint): void;
  emit(kind: number, a: bigint, b: bigint): void;
}

/** Bytes in the `sim_hud` record. */
export const HUD_BYTES = 28;

export type Hud = {
  readonly present: boolean;
  readonly checkedInToday: boolean;
  readonly day: number;
  readonly dogCount: number;
  readonly parkEnergy: bigint;
  readonly selfEnergy: number;
  readonly boosting: boolean;
};

const HUD_VERSION = 1;

/** Reads a `sim_hud` record. Returns null when the module wrote a version we don't know. */
export function decodeHud(bytes: Uint8Array): Hud | null {
  if (bytes.length < HUD_BYTES) return null;
  const dv = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  if (dv.getUint16(0, true) !== HUD_VERSION) return null;
  return {
    present: dv.getUint8(2) !== 0,
    checkedInToday: dv.getUint8(3) !== 0,
    day: dv.getUint32(4, true),
    dogCount: dv.getUint32(8, true),
    parkEnergy: dv.getBigUint64(12, true),
    selfEnergy: dv.getUint32(20, true),
    boosting: (dv.getUint8(24) & 2) !== 0,
  };
}

/**
 * Unpacks the `b` argument of telemetry code 2, `welcome(a = epoch,
 * b = hz | role << 32)`. The granted role rides here because it can be
 * narrower than the one the host asked for — a token that no longer
 * proves a player comes back as a spectator — and this is the only place
 * the session ABI reports it.
 */
export function decodeWelcomeEmit(b: bigint): { hz: number; role: 0 | 1 } {
  return {
    hz: Number(BigInt.asUintN(32, b)),
    role: Number(b >> 32n) === 1 ? 1 : 0,
  };
}

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
  /** 0 acquiring, 1 locked, 2 fast-forward, 3 snapshot-required. */
  readonly clockState: number;
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
    clockState: dv.getUint8(2),
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

/** `session_pump` status flags, in the low bits of its return. */
export const PumpFlag = {
  /** A world is live: the replica has state and is stepping it. */
  haveState: 1,
  /** A resync is outstanding; the core is waiting on a snapshot. */
  resyncing: 1 << 1,
  /** This pump ran at least one sim step. */
  stepped: 1 << 2,
  /** A snapshot is held back waiting for its terrain to load. */
  waitingTerrain: 1 << 3,
  /** A module swap is latched and waiting for the host to land it. */
  waitingModule: 1 << 4,
  /**
   * The world was corrected on this pump — a snapshot restored it, or a
   * repair rewound and replayed it — so dogs may have moved without time
   * passing. The host owes the screen a glide from where it last showed
   * them, on THIS frame: a frame later is a frame too late, because by
   * then the jump has been drawn. See `Core.view`.
   */
  corrected: 1 << 5,
} as const;

/** Where the clock state sits in `session_pump`'s return, above the flag bits. */
export const CLOCK_STATE_SHIFT = 8;

/** 0 acquiring, 1 locked, 2 fast-forward, 3 snapshot-required. */
export function clockStateOf(pumpFlags: number): number {
  return (pumpFlags >> CLOCK_STATE_SHIFT) & 3;
}

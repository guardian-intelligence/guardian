// The two wasm ABIs the host binds together, as TypeScript. The replica
// module is the simulation; the session module carries the session core,
// the clock, and the presentation smoother. Neither module imports the
// other — the host is the only thing that can see both linear memories,
// and copying between them is its whole job on the replica verbs. The
// `park_*` import names and `*_terrain*` export names are the frozen
// wire-level ABI spellings; the concepts they carry are the replica
// world and its content-addressed base blob.
//
// There is one world, and the replica verbs address it directly: what the
// session applies is what the renderer reads.
//
// wasm i64 crosses the boundary as bigint, so every u64/i64 here is a
// bigint and every u32/i32 a number. Getting that wrong is a silent
// truncation, not a type error, at the wasm boundary.
//
// Only the framework surface lives here. Game-specific exports — intent
// verbs on the session module, read projections on the replica module —
// go through `ReplicaHost.extension` and `ReplicaHost.readProjection`,
// so this file never has to know them.

/**
 * The ABI generation both modules stamp and this host expects. Additive
 * changes — a new export, a new emit code — never bump it; a removal or
 * re-typing must, so a mismatched pair refuses to boot instead of
 * throwing mid-frame.
 */
export const ABI_VERSION = 1;

/** The replica module's framework exports, as the host uses them. */
export interface ReplicaExports {
  readonly memory: WebAssembly.Memory;
  abi_version(): number;
  io_buf(): number;
  io_cap(): number;
  terrain_buf(): number;
  terrain_cap(): number;
  /** Adopts the blob already written into `terrain_buf`. 0 on success. */
  sim_set_terrain(len: number): number;
  /**
   * Creates a fresh world on the loaded blob. The host never calls
   * this — a client's world only ever arrives as a snapshot — but it is
   * part of the module's ABI and is what a test authority boots with.
   */
  sim_init(seed: bigint, worldId: bigint, epoch: number): number;
  /** The id of the blob currently loaded, as the wire and the blob route spell it. */
  sim_terrain_id(): bigint;
  /** Applies the event bytes in `io_buf` (leading kind u16). 0 on success. */
  sim_apply(len: number): number;
  sim_step(): void;
  /** Writes the snapshot state into `io_buf`; returns its length. */
  sim_snapshot(): number;
  /** Restores from `io_buf`. 0 on success; 4 means the loaded blob does not match. */
  sim_restore(len: number): number;
  sim_hash(): bigint;
  sim_tick(): bigint;
}

/** The session module's framework exports: the session core, plus the clock and smoother it links. */
export interface SessionExports {
  readonly memory: WebAssembly.Memory;

  abi_version(): number;

  /** Host staging area for inbound bytes and the outbound ticket. */
  session_buf(): number;
  session_cap(): number;

  session_init(actorId: bigint, role: number, checkMs: number, nonce: number, nowMs: bigint): void;
  /**
   * Adopts a new identity without discarding the replica: state and
   * `since_seq` survive, pending intents are dropped, and the fresh nonce
   * starts a new intent-id space so nothing from the old identity can be
   * mistaken for an ack.
   */
  session_reidentify(actorId: bigint, role: number, nonce: number, nowMs: bigint): void;
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
  /** Drives the clock, steps the world, applies ready events. Returns the pump word. */
  session_pump(nowMs: bigint, budgetUs: number): number;
  session_terrain_ready(id: bigint, ok: number): void;
  session_module_swapped(pw: number): void;
  /** Hidden pages stay silent: no check datagrams while `v` is 0. */
  session_set_visible(v: number): void;

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

// The canonical framework name tables. Interfaces do not survive to
// runtime, so these are what the boot check and the module-shape tests
// both verify against — one list per contract, mirrored nowhere else.

/** Exactly the members of `SessionExports`, minus `memory`. */
export const SESSION_EXPORTS = [
  "abi_version",
  "session_buf",
  "session_cap",
  "session_init",
  "session_reidentify",
  "session_connected",
  "session_disconnected",
  "session_on_stream",
  "session_on_datagram",
  "session_pump",
  "session_set_visible",
  "session_terrain_ready",
  "session_module_swapped",
  "session_phase_q16",
  "session_diag",
  "frame_buf",
  "frame_cap",
  "smooth_frame",
] as const;

/** Exactly the members of `HostImports`, all under import module `host`. */
export const HOST_IMPORTS = [
  "park_apply",
  "park_step",
  "park_snapshot",
  "park_restore",
  "park_hash",
  "park_tick",
  "send_stream",
  "send_datagram",
  "inflate",
  "request",
  "emit",
] as const;

/** Exactly the members of `ReplicaExports`, minus `memory`. */
export const REPLICA_EXPORTS = [
  "abi_version",
  "io_buf",
  "io_cap",
  "terrain_buf",
  "terrain_cap",
  "sim_set_terrain",
  "sim_init",
  "sim_terrain_id",
  "sim_apply",
  "sim_step",
  "sim_snapshot",
  "sim_restore",
  "sim_hash",
  "sim_tick",
] as const;

/**
 * How a compiled module fails the ABI, e.g. `["missing export sim_hash",
 * "unsatisfiable import env.now"]` — required exports the module lacks,
 * and imports it wants that the host does not supply. A module may import
 * less than the host offers (that is additive-safe); it may never export
 * less. Checked before any cast to a typed export surface: a missing
 * export found here is a refused boot, found later it is a TypeError on
 * every frame.
 */
export function abiViolations(
  module: WebAssembly.Module,
  required: { readonly exports: readonly string[]; readonly imports: readonly string[] },
): string[] {
  const exports = new Set(WebAssembly.Module.exports(module).map((e) => e.name));
  const supplied = new Set(required.imports);
  return [
    ...required.exports
      .filter((name) => !exports.has(name))
      .map((name) => `missing export ${name}`),
    ...WebAssembly.Module.imports(module)
      .filter((i) => i.module !== "host" || !supplied.has(i.name))
      .map((i) => `unsatisfiable import ${i.module}.${i.name}`),
  ];
}

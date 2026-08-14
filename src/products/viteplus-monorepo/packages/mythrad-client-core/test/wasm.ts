// The DST rig. Everything here is real: the committed `client.wasm` and
// `park.wasm`, the fixture terrain those modules are built against, and a
// second park instance standing in for the authority. Only the network
// and the clock are fakes, which is the point — the wire is the surface
// under test, so it is the only thing allowed to misbehave on purpose.
//
// The authority instance is what makes the assertions meaningful. Frames
// are minted from a park that actually applied the event, so a snapshot
// carries a hash the client can disagree with, and a "late" event is late
// against a world that really did move on.

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { deflateSync } from "fflate";
import { Core, type CoreOptions } from "../src/core.ts";
import type { ParkExports } from "../src/abi.ts";
import type { RoleName } from "../src/wire.ts";
import {
  Role,
  encodeEvent,
  encodeReject,
  encodeSnapshot,
  encodeVerdict,
  encodeWelcome,
  decodeCheck,
  hex64,
} from "../src/wire.ts";
import { Harness, type HarnessOptions } from "./fakes.ts";

/** Repo-relative, because these artifacts are committed and the suite must use those bytes. */
function repoFile(rel: string): Uint8Array {
  return new Uint8Array(
    readFileSync(fileURLToPath(new URL(`../../../../../${rel}`, import.meta.url))),
  );
}

const BEHAVIORS = "services/mythrad/behaviors/";

let cached: { client: Uint8Array; park: Uint8Array; terrain: Uint8Array } | null = null;

/** Reads the committed modules once per process; they are a few hundred KB. */
export function modules(): { client: Uint8Array; park: Uint8Array; terrain: Uint8Array } {
  cached ??= {
    client: repoFile(`${BEHAVIORS}client.wasm`),
    park: repoFile(`${BEHAVIORS}park.wasm`),
    terrain: repoFile("services/mythrad/terrain/fixture_park.bin"),
  };
  return cached;
}

/** Sim event kinds, as the park numbers them. */
export const Ev = {
  join: 1,
  depart: 2,
  checkIn: 3,
  moveTo: 4,
  epochAdvance: 6,
  boostSet: 8,
} as const;

/** Sim reject codes the session core gives special treatment. */
export const Reject = {
  present: 2,
  absent: 3,
  noop: 10,
  notYours: 101,
} as const;

export function dogPayload(id: bigint): Uint8Array {
  const p = new Uint8Array(8);
  new DataView(p.buffer).setBigUint64(0, id, true);
  return p;
}

function applyTo(park: ParkExports, kind: number, payload: Uint8Array): number {
  const buf = new Uint8Array(2 + payload.length);
  new DataView(buf.buffer).setUint16(0, kind, true);
  buf.set(payload, 2);
  new Uint8Array(park.memory.buffer).set(buf, park.io_buf());
  return park.sim_apply(buf.length);
}

/**
 * A park instance playing the server. It holds the canonical world, so
 * every frame it mints is one a real authority could have sent.
 */
export class Authority {
  readonly park: ParkExports;
  readonly terrain: Uint8Array;
  readonly terrainId: bigint;
  readonly terrainHex: string;
  seq = 0n;
  epoch = 1;
  hz = 24;
  parkName = "park-mythra";

  private constructor(park: ParkExports, terrain: Uint8Array, terrainId: bigint) {
    this.park = park;
    this.terrain = terrain;
    this.terrainId = terrainId;
    this.terrainHex = hex64(terrainId);
  }

  static async create(seed = 7n, parkId = 42n): Promise<Authority> {
    const m = modules();
    const { instance } = await WebAssembly.instantiate(m.park.slice().buffer);
    const park = instance.exports as unknown as ParkExports;
    new Uint8Array(park.memory.buffer).set(m.terrain, park.terrain_buf());
    const code = park.sim_set_terrain(m.terrain.length);
    if (code !== 0) throw new Error(`authority terrain rejected (code ${code})`);
    const init = park.sim_init(seed, parkId, 1);
    if (init !== 0) throw new Error(`authority init failed (code ${init})`);
    return new Authority(park, m.terrain, park.sim_terrain_id());
  }

  get tick(): bigint {
    return this.park.sim_tick();
  }

  hash(): bigint {
    return this.park.sim_hash();
  }

  step(n = 1): void {
    for (let i = 0; i < n; i++) this.park.sim_step();
  }

  welcome(role: number = Role.player): Uint8Array {
    return encodeWelcome({
      epoch: this.epoch,
      seq: this.seq,
      tick: this.tick,
      hz: this.hz,
      role,
      terrain: this.terrainId,
      park: this.parkName,
    });
  }

  /** Applies an event to the canonical world and returns the frame announcing it. */
  apply(kind: number, payload: Uint8Array, intent = 0n): Uint8Array {
    const code = applyTo(this.park, kind, payload);
    if (code !== 0) throw new Error(`authority refused kind ${kind} (code ${code})`);
    this.seq += 1n;
    return encodeEvent({ seq: this.seq, tick: this.tick, kind, intent, p: payload });
  }

  /**
   * A frame the authority never applied, stamped wherever the caller
   * wants. This is how a late, duplicated, or out-of-order delivery is
   * built without corrupting the canonical world.
   */
  frame(seq: bigint, tick: bigint, kind: number, payload: Uint8Array, intent = 0n): Uint8Array {
    return encodeEvent({ seq, tick, kind, intent, p: payload });
  }

  snapshot(): Uint8Array {
    const len = this.park.sim_snapshot();
    const at = this.park.io_buf();
    const state = new Uint8Array(this.park.memory.buffer.slice(at, at + len));
    return encodeSnapshot({
      seq: this.seq,
      tick: this.tick,
      epoch: this.epoch,
      wh: this.hash(),
      terrain: this.terrainId,
      z: deflateSync(state),
    });
  }

  /** A snapshot whose declared world hash is wrong, to exercise the mismatch path. */
  snapshotWithBadHash(): Uint8Array {
    const len = this.park.sim_snapshot();
    const at = this.park.io_buf();
    const state = new Uint8Array(this.park.memory.buffer.slice(at, at + len));
    return encodeSnapshot({
      seq: this.seq,
      tick: this.tick,
      epoch: this.epoch,
      wh: this.hash() ^ 1n,
      terrain: this.terrainId,
      z: deflateSync(state),
    });
  }

  /**
   * A snapshot whose FRAME names the terrain we hold, but whose state was
   * taken on another world. The core lands it straight away and the park
   * refuses with code 4 — the only way to reach the wrong-terrain path,
   * since a frame that names a different world is diverted to a fetch
   * first. The terrain id sits at bytes 52..60 of the MYP3 header.
   */
  snapshotWithWrongEmbeddedTerrain(): Uint8Array {
    const len = this.park.sim_snapshot();
    const at = this.park.io_buf();
    const state = new Uint8Array(this.park.memory.buffer.slice(at, at + len));
    new DataView(state.buffer).setBigUint64(52, this.terrainId ^ 0xffn, true);
    return encodeSnapshot({
      seq: this.seq,
      tick: this.tick,
      epoch: this.epoch,
      wh: this.hash(),
      terrain: this.terrainId,
      z: deflateSync(state),
    });
  }

  /** A snapshot claiming a world this client has never loaded. */
  snapshotForOtherTerrain(terrain: bigint): Uint8Array {
    const len = this.park.sim_snapshot();
    const at = this.park.io_buf();
    const state = new Uint8Array(this.park.memory.buffer.slice(at, at + len));
    return encodeSnapshot({
      seq: this.seq,
      tick: this.tick,
      epoch: this.epoch,
      wh: this.hash(),
      terrain,
      z: deflateSync(state),
    });
  }

  reject(intent: bigint, reason: number): Uint8Array {
    return encodeReject({ intent, reason });
  }

  /**
   * Answers a check. `now` on the wire is the authority's TICK, not a
   * timestamp — it is what the client's clock disciplines against. The
   * authority only holds its current hash, so `ok` is judged honestly for
   * a check about the current tick and must be stated explicitly for any
   * other.
   */
  verdict(
    check: { tick: bigint; wh: bigint; ctMs: bigint },
    over: { known?: boolean; ok?: boolean; cw?: Uint8Array; pw?: Uint8Array } = {},
  ): Uint8Array {
    const known = over.known ?? true;
    const honest = check.tick === this.tick && check.wh === this.hash();
    return encodeVerdict({
      tick: check.tick,
      now: this.tick,
      ctMs: check.ctMs,
      known,
      ok: over.ok ?? (known && honest),
      cw: over.cw ?? new Uint8Array(4),
      pw: over.pw ?? new Uint8Array(4),
    });
  }
}

export type RigOptions = HarnessOptions & {
  readonly myDog?: bigint;
  readonly role?: RoleName;
  readonly checkMs?: number;
  /** Withhold the terrain fixture, to exercise the fetch-failure lane. */
  readonly withoutTerrain?: boolean;
};

export type Rig = {
  readonly core: Core;
  readonly harness: Harness;
  readonly authority: Authority;
  /** Advance time and pump, letting fetch promises resolve between frames. */
  run(ms: number, stepMs?: number): Promise<void>;
  /** Pump until `done()` holds or `ms` of virtual time elapses. Returns whether it held. */
  until(done: () => boolean, ms?: number, stepMs?: number): Promise<boolean>;
  /** Deliver server frames, optionally chopped into the given read sizes. */
  deliver(frames: Uint8Array[], chunkSizes?: number[]): void;
  /** The whole handshake: welcome, terrain, snapshot, and a world that is stepping. */
  establish(role?: number): Promise<void>;
  /**
   * Applies an event on the authority a few ticks ahead of the client and
   * returns its frame. Stamping it in the client's future is the ordinary
   * case — the client queues it and applies it on arrival at that tick,
   * with no rollback. Use `authority.frame` directly to stamp one late.
   */
  emit(kind: number, payload: Uint8Array, intent?: bigint, lead?: number): Uint8Array;
  /**
   * Answers every check datagram not yet answered, and returns how many.
   * `over` forces the verdict's judgement — a real authority decides, but
   * a test needs to be able to lie.
   */
  answerChecks(over?: { known?: boolean; ok?: boolean; cw?: Uint8Array; pw?: Uint8Array }): number;
  /** Waits until at least `n` check datagrams have been sent. */
  waitForChecks(n: number, ms?: number): Promise<boolean>;
  /** Telemetry codes seen so far, for asserting a choreography. */
  codes(): number[];
  /** How many times a telemetry code has been emitted. */
  count(code: number): number;
};

/** The 12-byte epoch_advance payload: new epoch u32, then the module sum u64. */
export function epochAdvancePayload(epoch: number, moduleSum: bigint): Uint8Array {
  const p = new Uint8Array(12);
  const dv = new DataView(p.buffer);
  dv.setUint32(0, epoch, true);
  dv.setBigUint64(4, moduleSum, true);
  return p;
}

/**
 * Boots a `Core` against the real modules with every port faked. Nothing
 * has been delivered yet — the caller drives the session frame by frame.
 */
export async function rig(options: RigOptions = {}): Promise<Rig> {
  const m = modules();
  const authority = await Authority.create();
  const harness = new Harness({
    ...options,
    modules: { park: m.park.slice().buffer, client: m.client.slice().buffer },
    terrain: options.withoutTerrain
      ? {}
      : { [authority.terrainHex]: authority.terrain.slice().buffer },
  });
  const coreOptions: CoreOptions = {
    myDog: options.myDog ?? 0x1122_3344_5566_7788n,
    role: options.role ?? "player",
    park: "park-mythra",
    checkMs: options.checkMs ?? 5000,
    schedule: harness.schedule,
  };
  const core = new Core(harness.ports, coreOptions);
  await core.boot();
  await harness.settle();

  // The authority runs the same module on the same events, so it must
  // also run the same ticks: a server that never stepped would make every
  // event look late and every hash look wrong. It keeps pace with the
  // client's replica rather than a wall clock of its own.
  const syncWorld = (): void => {
    while (authority.tick < core.state.tick) authority.step();
  };

  const run = async (ms: number, stepMs = 8): Promise<void> => {
    for (let t = 0; t < ms; t += stepMs) {
      harness.clock.advance(stepMs);
      core.pump();
      syncWorld();
      await harness.settle();
    }
  };

  const until = async (done: () => boolean, ms = 4000, stepMs = 8): Promise<boolean> => {
    for (let t = 0; t < ms; t += stepMs) {
      if (done()) return true;
      harness.clock.advance(stepMs);
      core.pump();
      syncWorld();
      await harness.settle();
    }
    return done();
  };

  const emit = (kind: number, payload: Uint8Array, intent = 0n, lead = 3): Uint8Array => {
    const target = core.state.tick + BigInt(lead);
    while (authority.tick < target) authority.step();
    return authority.apply(kind, payload, intent);
  };

  const deliver = (frames: Uint8Array[], chunkSizes?: number[]): void => {
    harness.transport.deliverFrames(frames, chunkSizes);
  };

  const establish = async (role: number = Role.player): Promise<void> => {
    deliver([authority.welcome(role)]);
    // The welcome asks for terrain; the fetch resolves on the microtask
    // queue and the snapshot cannot land until it has.
    await until(() => core.state.hz > 0, 200);
    deliver([authority.snapshot()]);
    const landed = await until(
      () => core.state.seq === authority.seq && core.state.tick > 0n,
      2000,
    );
    if (!landed) throw new Error("world never landed");
  };

  let answered = 0;
  const answerChecks: Rig["answerChecks"] = (over = {}) => {
    const sent = harness.transport.sentDatagrams;
    let n = 0;
    for (; answered < sent.length; answered++, n++) {
      harness.transport.deliverDatagram(authority.verdict(decodeCheck(sent[answered]!), over));
    }
    return n;
  };

  const waitForChecks = (n: number, ms = 6000): Promise<boolean> =>
    until(() => harness.transport.sentDatagrams.length >= n, ms);

  const codes = (): number[] => harness.emitted.map((e) => e.code);
  const count = (code: number): number =>
    harness.emitted.reduce((n, e) => n + (e.code === code ? 1 : 0), 0);

  return {
    core,
    harness,
    authority,
    run,
    until,
    deliver,
    establish,
    emit,
    answerChecks,
    waitForChecks,
    codes,
    count,
  };
}

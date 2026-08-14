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
import type { ClientFrame } from "../src/wire.ts";

/** Repo-relative, because these artifacts are committed and the suite must use those bytes. */
function repoFile(rel: string): Uint8Array {
  return new Uint8Array(
    readFileSync(fileURLToPath(new URL(`../../../../../${rel}`, import.meta.url))),
  );
}

const BEHAVIORS = "services/mythrad/behaviors/";

type Modules = {
  client: Uint8Array;
  park: Uint8Array;
  terrain: Uint8Array;
  /**
   * The behavior-slot module. It instantiates cleanly and exports almost
   * nothing, which makes it the honest stand-in for "a park module this
   * host cannot put a world into": the failure lands AFTER instantiation,
   * which is the ordering a module swap has to get right.
   */
  server: Uint8Array;
};

let cached: Modules | null = null;

/** Reads the committed modules once per process; they are a few hundred KB. */
export function modules(): Modules {
  cached ??= {
    client: repoFile(`${BEHAVIORS}client.wasm`),
    park: repoFile(`${BEHAVIORS}park.wasm`),
    server: repoFile(`${BEHAVIORS}server.wasm`),
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
  /**
   * World hash per tick, as the state stood on ENTRY to it — before any
   * event stamped for that tick. A real park keeps this ring so it can
   * answer a check about a tick it has already passed; without it every
   * verdict about anything but the current instant reads as a mismatch,
   * and the client is told its world is wrong when it is not.
   */
  readonly #hashes = new Map<bigint, bigint>();

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
    const authority = new Authority(park, m.terrain, park.sim_terrain_id());
    authority.seedHash();
    return authority;
  }

  /** Records the starting world, so tick 0 is answerable like any other. */
  seedHash(): void {
    this.#recordHash();
  }

  get tick(): bigint {
    return this.park.sim_tick();
  }

  /**
   * The world hash, unsigned. `sim_hash` is declared i64, so wasm hands
   * JS a SIGNED bigint, while the wire carries the same bits unsigned —
   * comparing the two directly reports a mismatch for every hash with its
   * top bit set, which is half of them.
   */
  hash(): bigint {
    return BigInt.asUintN(64, this.park.sim_hash());
  }

  step(n = 1): void {
    for (let i = 0; i < n; i++) {
      this.park.sim_step();
      this.#recordHash();
    }
  }

  #recordHash(): void {
    this.#hashes.set(this.tick, this.hash());
    // Bounded like the real one; long scenarios would otherwise grow it
    // without limit.
    if (this.#hashes.size > 2048) {
      const oldest = this.#hashes.keys().next().value;
      if (oldest !== undefined) this.#hashes.delete(oldest);
    }
  }

  /** What this park held on entry to `tick`, if it still remembers. */
  hashAt(tick: bigint): bigint | undefined {
    return this.#hashes.get(tick);
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
    // The tick the event is journalled AT, read before the event can move
    // it. Only `clock_skip` does, and stamping one with its own
    // destination would announce it from a tick the park had not reached.
    const at = this.tick;
    const code = applyTo(this.park, kind, payload);
    if (code !== 0) throw new Error(`authority refused kind ${kind} (code ${code})`);
    this.seq += 1n;
    return encodeEvent({ seq: this.seq, tick: at, kind, intent, p: payload });
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

  /** Fills the roster to MAX_DOGS so the snapshot is the largest a park can emit. */
  fillRoster(): number {
    let joined = 0;
    for (let i = 1; i <= 2048; i++) {
      if (applyTo(this.park, Ev.join, dogPayload(BigInt(i))) === 0) joined++;
    }
    this.seq += BigInt(joined);
    return joined;
  }

  /**
   * A snapshot whose payload is stored-block DEFLATE: still a valid raw
   * stream that inflates to a real state and lands, but roughly the size
   * of the state itself. Compression would otherwise take a full 2048-dog
   * park from 61.5 KB down to about 12 KB, and the point of this frame is
   * to be large on the wire.
   */
  snapshotUncompressed(): Uint8Array {
    const len = this.park.sim_snapshot();
    const at = this.park.io_buf();
    const state = new Uint8Array(this.park.memory.buffer.slice(at, at + len));
    return encodeSnapshot({
      seq: this.seq,
      tick: this.tick,
      epoch: this.epoch,
      wh: this.hash(),
      terrain: this.terrainId,
      z: deflateSync(state, { level: 0 }),
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
    const mine = this.hashAt(check.tick);
    const known = over.known ?? mine !== undefined;
    const honest = mine !== undefined && mine === check.wh;
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

/**
 * The round trip every verdict takes unless a test asks for another. A
 * verdict that answers instantly describes a network that does not exist,
 * and compensating for the one that does is the clock's whole job.
 */
export const DEFAULT_RTT_MS = 120;

export type RigOptions = HarnessOptions & {
  /** Round trip for verdicts. Defaults to `DEFAULT_RTT_MS`. */
  readonly rttMs?: number;
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
  /**
   * Answers every outstanding check over a round trip: the authority sees
   * the check after half of `rttMs` and stamps its verdict with the tick
   * it holds THEN, and the client sees the answer half a trip later. An
   * instant answer hides exactly the drift a clock exists to correct.
   */
  answerChecksOverRtt(
    rttMs: number,
    over?: { known?: boolean; ok?: boolean; cw?: Uint8Array; pw?: Uint8Array },
  ): number;
  /** Waits until at least `n` check datagrams have been sent. */
  waitForChecks(n: number, ms?: number): Promise<boolean>;
  /**
   * Answers any resync the core has asked for and not been given, the way
   * a park does: a snapshot delivered IN-STREAM, a round trip later.
   *
   * In-stream is the whole of it. The authority queues its snapshot into
   * the same ordered stream as the events, so everything already sent
   * arrived ahead of it and is folded into the state it carries — which is
   * why the core is right to drop what it had queued when one lands. A
   * harness that answers out of band stops modelling that, and a harness
   * that does not answer at all leaves the session holding everything it
   * has been sent, waiting for a snapshot that is never coming. Both look
   * exactly like a permanently stalled client, and neither is one.
   *
   * Returns how many requests it answered.
   */
  answerResyncs(): number;
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
  const answerChecks: Rig["answerChecks"] = (over = {}) =>
    answerChecksOverRtt(options.rttMs ?? DEFAULT_RTT_MS, over);

  const answerChecksOverRtt: Rig["answerChecksOverRtt"] = (rttMs, over = {}) => {
    const sent = harness.transport.sentDatagrams;
    let n = 0;
    for (; answered < sent.length; answered++, n++) {
      const check = decodeCheck(sent[answered]!);
      harness.clock.schedule(() => {
        // Stamped with the world as the authority holds it on arrival.
        const verdict = authority.verdict(check, over);
        harness.clock.schedule(() => {
          try {
            harness.transport.deliverDatagram(verdict);
          } catch {
            // The connection went away mid-flight; the datagram is lost,
            // which is the contract.
          }
        }, rttMs / 2);
      }, rttMs / 2);
    }
    return n;
  };

  const waitForChecks = (n: number, ms = 6000): Promise<boolean> =>
    until(() => harness.transport.sentDatagrams.length >= n, ms);

  let resyncsAnswered = 0;
  const answerResyncs: Rig["answerResyncs"] = () => {
    const asked = harness.transport.sentFrames().filter((f) => f.kind === "resync").length;
    let n = 0;
    for (; resyncsAnswered < asked; resyncsAnswered++, n++) {
      harness.clock.schedule(() => {
        try {
          deliver([authority.snapshot()]);
        } catch {
          // The connection went away before the answer arrived, which is
          // one of the things a resync has to survive.
        }
      }, options.rttMs ?? DEFAULT_RTT_MS);
    }
    return n;
  };

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
    answerChecksOverRtt,
    waitForChecks,
    answerResyncs,
    codes,
    count,
  };
}

/** Intent frames of one kind the client has written, oldest first. */
export function intentsSent(r: Rig, kind: number): ClientFrame[] {
  return r.harness.transport
    .sentFrames()
    .filter((f) => f.kind === "intent" && f.value.kind === kind);
}

export function intentId(frame: ClientFrame | undefined): bigint {
  return frame?.kind === "intent" ? frame.value.id : 0n;
}

/**
 * Answers the join the core sent on connect, so the dog is really in the
 * park. Intents that need presence are held until this has happened, and
 * the journal is the only thing that grants it.
 */
export async function bringTheDogIn(r: Rig, dog: bigint): Promise<void> {
  await r.run(100);
  const id = intentId(intentsSent(r, Ev.join)[0]);
  r.deliver([r.emit(Ev.join, dogPayload(dog), id)]);
  const present = await r.until(() => r.core.state.present, 2000);
  if (!present) throw new Error("the journal never placed the dog");
}

/**
 * The other way a dog's presence is established: the park already holds
 * it, so the reconnecting join is answered "already present" rather than
 * with the journal event that placed it.
 */
export async function bringTheDogInViaPresent(r: Rig, dog: bigint): Promise<void> {
  await r.run(100);
  // The park already holds this dog — that is what makes the join it just
  // received a duplicate. So the world the client is given contains it,
  // and the reject is what tells the SESSION the dog is there: the
  // journal event that placed it happened before this connection existed
  // and is never coming.
  r.authority.apply(Ev.join, dogPayload(dog));
  r.deliver([r.authority.snapshot()]);
  const inWorld = await r.until(() => r.core.state.present, 2000);
  if (!inWorld) throw new Error("the snapshot did not carry the dog");
  const id = intentId(intentsSent(r, Ev.join).at(-1));
  r.deliver([r.authority.reject(id, Reject.present)]);
  await r.run(200);
}

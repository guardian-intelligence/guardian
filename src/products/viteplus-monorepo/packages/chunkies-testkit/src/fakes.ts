// Deterministic stand-ins for every port. Nothing here reads a real
// clock, a real network, or a real entropy source, so a run is a pure
// function of its script — which is what makes the DST suite able to
// shrink a failure to a seed and a delivery order.
//
import type {
  BehaviorModule,
  Connection,
  ConnectionSink,
  Dialed,
  Ports,
  TransportPort,
} from "@guardian/mythrad-client-core";
import { FrameDecoder, decodeClientFrame, type ClientFrame } from "./wire.ts";

/** A clock that only moves when the test says so, plus the timers hanging off it. */
export class VirtualClock {
  #now: number;
  #seq = 0;
  #timers = new Map<number, { at: number; fn: () => void }>();

  constructor(startMs = 1_700_000_000_000) {
    this.#now = startMs;
  }

  now(): number {
    return this.#now;
  }

  /** The `schedule` port: returns its own canceller. */
  readonly schedule = (fn: () => void, ms: number): (() => void) => {
    const id = this.#seq++;
    this.#timers.set(id, { at: this.#now + ms, fn });
    return () => {
      this.#timers.delete(id);
    };
  };

  /** Milliseconds until the next timer, or null when none is pending. */
  get nextDue(): number | null {
    let soonest: number | null = null;
    for (const t of this.#timers.values()) {
      if (soonest === null || t.at < soonest) soonest = t.at;
    }
    return soonest === null ? null : soonest - this.#now;
  }

  get pending(): number {
    return this.#timers.size;
  }

  /**
   * Moves time forward, firing every timer that comes due, in due order.
   * Timers scheduled by a firing timer are picked up in the same advance
   * if they also come due — a redial storm plays out in one call.
   */
  advance(ms: number): void {
    const until = this.#now + ms;
    for (;;) {
      let next: [number, { at: number; fn: () => void }] | null = null;
      for (const entry of this.#timers) {
        if (entry[1].at > until) continue;
        if (next === null || entry[1].at < next[1].at) next = entry;
      }
      if (next === null) break;
      this.#timers.delete(next[0]);
      this.#now = next[1].at;
      next[1].fn();
    }
    this.#now = until;
  }
}

/**
 * xorshift32. Same seed, same jitter, same backoff schedule, every run.
 *
 * TEST ONLY. A shipping host must inject `crypto.getRandomValues` for
 * `random32` — see the contract on `Ports.random32`, where a predictable
 * nonce is a correctness bug, not a quality one. Reproducibility is the
 * whole point here and exactly the wrong property in production.
 */
export function seededRandom32(seed: number): () => number {
  let s = seed >>> 0 || 0x9e3779b9;
  return () => {
    s ^= s << 13;
    s >>>= 0;
    s ^= s >>> 17;
    s ^= s << 5;
    s >>>= 0;
    return s;
  };
}

export type DialOutcome = { readonly ok: true } | { readonly ok: false; readonly error: string };

/**
 * A transport the test drives byte by byte. It records everything the
 * core sends, and lets the test decide how server bytes are chopped up
 * and in what order datagrams arrive.
 */
export class ScriptedTransport implements TransportPort {
  /** Consumed one per dial; the last entry repeats once exhausted. */
  outcomes: DialOutcome[] = [{ ok: true }];
  ticket = new Uint8Array([0x74, 0x6b, 0x74]);

  dials = 0;
  readonly sentStream: Uint8Array[] = [];
  readonly sentDatagrams: Uint8Array[] = [];
  closed = 0;

  #sink: ConnectionSink | null = null;
  #live = false;

  async connect(sink: ConnectionSink): Promise<Dialed> {
    const outcome = this.outcomes[Math.min(this.dials, this.outcomes.length - 1)] ?? { ok: true };
    this.dials++;
    if (!outcome.ok) throw new Error(outcome.error);
    this.#sink = sink;
    this.#live = true;
    const connection: Connection = {
      sendStream: (b) => this.sentStream.push(b.slice()),
      sendDatagram: (b) => this.sentDatagrams.push(b.slice()),
      close: () => {
        this.closed++;
        this.#live = false;
      },
    };
    return { connection, ticket: this.ticket };
  }

  get live(): boolean {
    return this.#live;
  }

  /** Delivers stream bytes, optionally chopped into the given read sizes. */
  deliverStream(bytes: Uint8Array, chunkSizes?: number[]): void {
    if (!this.#sink) throw new Error("no connection to deliver on");
    for (const piece of chop(bytes, chunkSizes)) this.#sink.onStreamBytes(piece);
  }

  /** Delivers whole frames in the given order — reorder the array to reorder the wire. */
  deliverFrames(frames: Uint8Array[], chunkSizes?: number[]): void {
    this.deliverStream(concatBytes(frames), chunkSizes);
  }

  deliverDatagram(bytes: Uint8Array): void {
    if (!this.#sink) throw new Error("no connection to deliver on");
    this.#sink.onDatagram(bytes);
  }

  /** The server (or the network) went away. */
  drop(): void {
    this.#live = false;
    const sink = this.#sink;
    this.#sink = null;
    sink?.onClosed();
  }

  /** Everything the core has written to the stream, decoded. */
  sentFrames(): ClientFrame[] {
    const d = new FrameDecoder();
    const out: ClientFrame[] = [];
    for (const chunk of this.sentStream) {
      for (const f of d.push(chunk)) out.push(decodeClientFrame(f.kind, f.body));
    }
    return out;
  }
}

/** Splits `bytes` into reads of the given sizes; the remainder rides the last read. */
export function chop(bytes: Uint8Array, sizes?: number[]): Uint8Array[] {
  if (!sizes || sizes.length === 0) return [bytes];
  const out: Uint8Array[] = [];
  let at = 0;
  for (const size of sizes) {
    if (at >= bytes.length) break;
    out.push(bytes.subarray(at, Math.min(at + size, bytes.length)));
    at += size;
  }
  if (at < bytes.length) out.push(bytes.subarray(at));
  return out;
}

export function concatBytes(parts: Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let at = 0;
  for (const p of parts) {
    out.set(p, at);
    at += p.length;
  }
  return out;
}

export type Emitted = { readonly code: number; readonly a: bigint; readonly b: bigint };

export type HarnessOptions = {
  readonly seed?: number;
  readonly startMs?: number;
  /** Module bytes by kind. Absent entries make `fetchBehavior` reject. */
  readonly modules?: Partial<Record<BehaviorModule, ArrayBuffer>>;
  /** Terrain artifacts by 16-hex-char id. Absent entries make `fetchTerrain` reject. */
  readonly terrain?: Record<string, ArrayBuffer>;
};

/** Every port, wired to the deterministic fakes, plus the recordings a test asserts on. */
export class Harness {
  readonly clock: VirtualClock;
  readonly transport = new ScriptedTransport();
  readonly emitted: Emitted[] = [];
  readonly logs: string[] = [];
  readonly behaviorFetches: { kind: BehaviorModule; ref?: string }[] = [];
  readonly terrainFetches: string[] = [];
  readonly ports: Ports;
  readonly schedule: (fn: () => void, ms: number) => () => void;

  #modules: Partial<Record<BehaviorModule, ArrayBuffer>>;
  #terrain: Record<string, ArrayBuffer>;
  #random: () => number;

  constructor(options: HarnessOptions = {}) {
    this.clock = new VirtualClock(options.startMs);
    this.schedule = this.clock.schedule;
    this.#modules = options.modules ?? {};
    this.#terrain = options.terrain ?? {};
    this.#random = seededRandom32(options.seed ?? 1);
    this.ports = {
      transport: this.transport,
      fetchBehavior: (kind, ref) => {
        this.behaviorFetches.push(ref === undefined ? { kind } : { kind, ref });
        const bytes = this.#modules[kind];
        return bytes ? Promise.resolve(bytes) : Promise.reject(new Error(`no ${kind} module`));
      },
      fetchTerrain: (idHex) => {
        this.terrainFetches.push(idHex);
        const bytes = this.#terrain[idHex];
        return bytes ? Promise.resolve(bytes) : Promise.reject(new Error(`no terrain ${idHex}`));
      },
      now: () => this.clock.now(),
      telemetry: (code, a, b) => {
        this.emitted.push({ code, a, b });
      },
      random32: () => this.#random(),
      // Node has webcrypto on the global, so this matches the browser
      // default rather than standing in for it.
      sha256: (bytes) => crypto.subtle.digest("SHA-256", bytes),
      log: (line) => {
        this.logs.push(line);
      },
    };
  }

  /** Swaps what `fetchBehavior` serves, so a test can change what a re-fetch returns. */
  setModule(kind: BehaviorModule, bytes: ArrayBuffer): void {
    this.#modules = { ...this.#modules, [kind]: bytes };
  }

  /** Codes emitted so far, for asserting a choreography without pinning arguments. */
  codes(): number[] {
    return this.emitted.map((e) => e.code);
  }

  /**
   * Lets pending async work finish. Microtask drains alone are not
   * enough: instantiating a module and hashing it go through host
   * promises that settle on the macrotask queue, and a module swap
   * chains several. The real timer here is safe — the core's own timers
   * come from the injected scheduler, so nothing virtual advances.
   */
  async settle(turns = 4): Promise<void> {
    for (let i = 0; i < turns; i++) await Promise.resolve();
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
}

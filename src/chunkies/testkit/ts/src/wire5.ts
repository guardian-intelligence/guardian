// chunkies wire protocol v5: length-prefixed binary frames on a
// subscription stream, fixed-layout datagrams beside it. Every scalar is
// little-endian; the only big-endian quantity is the QUIC variable-length
// integer carrying a frame's length (RFC 9000 §16).
//
// This module is pure bytes in, values out, and dev/test-only like the
// rest of this package: production TS never sees wire bytes (the Rust
// session core owns them). It exists so harnesses can mint and read v5
// frames, held to the shared golden vectors in
// src/chunkies/codec/spec/vectors.txt (mirrored into this
// package's goldens/ under Bazel lockstep). The vectors are the spec;
// the implementations are not.
//
// The protocol's one shared unit is the EventRecord —
// `intent u64 | elen u16 | SimEvent`, SimEvent = `kind u16 | actor u64 |
// payload` — the same bytes serving as intent envelope, tick batch
// element, and (authority-side) write-ahead record element. Decoders here
// reject non-canonical input rather than normalize it.

export const PROTO5 = 5;

/** One frame body (kind byte plus payload); spec/caps.txt is the source of truth. */
export const MAX_FRAME = 128 * 1024;

/** The datagram class bound; 1200 is the QUIC minimum path MTU floor. */
export const DG_MAX = 1200;

export const Frame5 = {
  hello: 1,
  intent: 2,
  resync: 3,
  welcome: 16,
  // 17 was v4's per-event kind and stays retired with its layout.
  reject: 18,
  snapshot: 19,
  tick: 20,
} as const;

export const Datagram5 = {
  check: 1,
  verdict: 2,
} as const;

export const HELLO_HEADER = 24;
export const RESYNC_BYTES = 12;
export const WELCOME_HEADER = 46;
export const TICK_HEADER = 18;
export const REJECT_BYTES = 12;
export const SNAPSHOT_HEADER = 44;
export const CHECK_BYTES = 29;
export const VERDICT_BYTES = 42;

export const EVENT_RECORD_HEADER = 10;
export const SIM_EVENT_HEADER = 10;

/** The reserved intent id for authority-minted records; a client intent must be nonzero. */
export const SYSTEM_INTENT = 0n;

export const VERDICT_KNOWN = 1;
export const VERDICT_OK = 2;

export class Wire5Error extends Error {}

function fail(msg: string): never {
  throw new Wire5Error(msg);
}

// ---------- little-endian building ----------

class Builder {
  private out: number[] = [];

  u8(v: number): this {
    this.out.push(v & 0xff);
    return this;
  }
  u16(v: number): this {
    return this.u8(v).u8(v >>> 8);
  }
  u32(v: number): this {
    return this.u16(v).u16(v >>> 16);
  }
  u64(v: bigint): this {
    const u = BigInt.asUintN(64, v);
    return this.u32(Number(u & 0xffffffffn)).u32(Number(u >> 32n));
  }
  bytes(b: Uint8Array): this {
    for (const x of b) this.out.push(x);
    return this;
  }
  take(): Uint8Array {
    return Uint8Array.from(this.out);
  }
}

/** RFC 9000 §16 varint, shortest form (big-endian with a 2-bit tag). */
export function encodeVarint(v: number): Uint8Array {
  if (v < 0 || !Number.isSafeInteger(v)) fail(`varint: bad value ${v}`);
  if (v <= 0x3f) return Uint8Array.of(v);
  if (v <= 0x3fff) return Uint8Array.of((v >>> 8) | 0x40, v & 0xff);
  if (v <= 0x3fffffff) {
    return Uint8Array.of(((v >>> 24) & 0x3f) | 0x80, (v >>> 16) & 0xff, (v >>> 8) & 0xff, v & 0xff);
  }
  const hi = Math.floor(v / 2 ** 32);
  return Uint8Array.of(
    ((hi >>> 24) & 0x3f) | 0xc0,
    (hi >>> 16) & 0xff,
    (hi >>> 8) & 0xff,
    hi & 0xff,
    (v >>> 24) & 0xff,
    (v >>> 16) & 0xff,
    (v >>> 8) & 0xff,
    v & 0xff,
  );
}

/** Decodes a varint at `at`; any of the four forms, as RFC 9000 permits. */
export function decodeVarint(b: Uint8Array, at = 0): { value: number; length: number } | null {
  const first = b[at];
  if (first === undefined) return null;
  const len = 1 << (first >> 6);
  if (at + len > b.length) return null;
  let v = first & 0x3f;
  for (let i = 1; i < len; i++) v = v * 256 + (b[at + i] ?? 0);
  return { value: v, length: len };
}

function frame(kind: number, payload: Uint8Array): Uint8Array {
  const body = 1 + payload.length;
  const pre = encodeVarint(body);
  const out = new Uint8Array(pre.length + body);
  out.set(pre, 0);
  out[pre.length] = kind;
  out.set(payload, pre.length + 1);
  return out;
}

/**
 * Splits a whole frame into kind and payload, strictly: the declared
 * length must cover the buffer exactly, and a body over MAX_FRAME is a
 * peer that lost the framing.
 */
export function splitFrame(b: Uint8Array): { kind: number; payload: Uint8Array } {
  const pre = decodeVarint(b);
  if (pre === null) fail("frame: short varint");
  if (pre.value === 0 || pre.value > MAX_FRAME) fail(`frame: bad body length ${pre.value}`);
  if (pre.length + pre.value !== b.length) fail("frame: length disagrees with buffer");
  return {
    kind: b[pre.length] ?? fail("frame: no kind byte"),
    payload: b.subarray(pre.length + 1),
  };
}

// ---------- reading ----------

class Cursor {
  private at = 0;
  constructor(
    private readonly b: Uint8Array,
    private readonly dv = new DataView(b.buffer, b.byteOffset, b.byteLength),
  ) {}

  private need(n: number): number {
    if (this.at + n > this.b.length) fail("payload: truncated field");
    const at = this.at;
    this.at += n;
    return at;
  }
  u8(): number {
    return this.b[this.need(1)] ?? fail("payload: truncated field");
  }
  u16(): number {
    return this.dv.getUint16(this.need(2), true);
  }
  u32(): number {
    return this.dv.getUint32(this.need(4), true);
  }
  u64(): bigint {
    return this.dv.getBigUint64(this.need(8), true);
  }
  i64(): bigint {
    return BigInt.asIntN(64, this.u64());
  }
  bytes(n: number): Uint8Array {
    const at = this.need(n);
    return this.b.subarray(at, at + n);
  }
  /** Every field present and nothing trailing — the exactness rule. */
  done(): void {
    if (this.at !== this.b.length) fail("payload: trailing bytes");
  }
}

// ---------- the EventRecord ----------

export interface EventRecord {
  intent: bigint;
  kind: number;
  actor: bigint;
  payload: Uint8Array;
  /** The trailing `kind | actor | payload` bytes — what an apply receives. */
  simEvent: Uint8Array;
}

export function encodeEventRecord(
  intent: bigint,
  kind: number,
  actor: bigint,
  payload: Uint8Array,
): Uint8Array {
  if (payload.length > 0xffff - SIM_EVENT_HEADER) fail("record: payload exceeds u16 framing");
  return new Builder()
    .u64(intent)
    .u16(SIM_EVENT_HEADER + payload.length)
    .u16(kind)
    .u64(actor)
    .bytes(payload)
    .take();
}

function readRecord(b: Uint8Array, at: number): { rec: EventRecord; next: number } | null {
  if (at + EVENT_RECORD_HEADER > b.length) return null;
  const dv = new DataView(b.buffer, b.byteOffset, b.byteLength);
  const elen = dv.getUint16(at + 8, true);
  const next = at + EVENT_RECORD_HEADER + elen;
  if (elen < SIM_EVENT_HEADER || next > b.length) return null;
  const simEvent = b.subarray(at + EVENT_RECORD_HEADER, next);
  return {
    rec: {
      intent: dv.getBigUint64(at, true),
      kind: dv.getUint16(at + EVENT_RECORD_HEADER, true),
      actor: dv.getBigUint64(at + EVENT_RECORD_HEADER + 2, true),
      payload: simEvent.subarray(SIM_EVENT_HEADER),
      simEvent,
    },
    next,
  };
}

/** Validates a run of exactly `count` records covering all of `b`. */
export function decodeRecords(b: Uint8Array, count: number): EventRecord[] {
  const out: EventRecord[] = [];
  let at = 0;
  while (out.length < count) {
    const r = readRecord(b, at);
    if (r === null) fail("record run: malformed record");
    out.push(r.rec);
    at = r.next;
  }
  if (at !== b.length) fail("record run: trailing bytes");
  return out;
}

// ---------- messages ----------

export interface Hello5 {
  proto: number;
  sinceLineage: number;
  sinceSeq: bigint;
  sinceTick: bigint;
  ticket: Uint8Array;
}

export function encodeHello(f: Hello5): Uint8Array {
  return frame(
    Frame5.hello,
    new Builder()
      .u16(f.proto)
      .u32(f.sinceLineage)
      .u64(f.sinceSeq)
      .u64(f.sinceTick)
      .u16(f.ticket.length)
      .bytes(f.ticket)
      .take(),
  );
}

export function decodeHello(p: Uint8Array): Hello5 {
  const c = new Cursor(p);
  const f = {
    proto: c.u16(),
    sinceLineage: c.u32(),
    sinceSeq: c.i64(),
    sinceTick: c.u64(),
    ticket: c.bytes(c.u16()),
  };
  c.done();
  return f;
}

export function encodeIntent(
  intent: bigint,
  kind: number,
  actor: bigint,
  payload: Uint8Array,
): Uint8Array {
  return frame(Frame5.intent, encodeEventRecord(intent, kind, actor, payload));
}

export function decodeIntent(p: Uint8Array): EventRecord {
  const r = readRecord(p, 0);
  if (r === null || r.next !== p.length) fail("intent: malformed record");
  if (r.rec.intent === SYSTEM_INTENT) fail("intent: zero id is the authority's to mint");
  return r.rec;
}

export interface Resync5 {
  lineage: number;
  haveSeq: bigint;
}

export function encodeResync(f: Resync5): Uint8Array {
  return frame(Frame5.resync, new Builder().u32(f.lineage).u64(f.haveSeq).take());
}

export function decodeResync(p: Uint8Array): Resync5 {
  const c = new Cursor(p);
  const f = { lineage: c.u32(), haveSeq: c.i64() };
  c.done();
  return f;
}

export interface Welcome5 {
  lineage: number;
  generation: number;
  sub: number;
  epoch: number;
  seq: bigint;
  tick: bigint;
  hz: number;
  role: number;
  content: bigint;
  chunk: string;
}

export function encodeWelcome(f: Welcome5): Uint8Array {
  const chunk = new TextEncoder().encode(f.chunk).subarray(0, 255);
  return frame(
    Frame5.welcome,
    new Builder()
      .u32(f.lineage)
      .u32(f.generation)
      .u32(f.sub)
      .u32(f.epoch)
      .u64(f.seq)
      .u64(f.tick)
      .u32(f.hz)
      .u8(f.role)
      .u64(f.content)
      .u8(chunk.length)
      .bytes(chunk)
      .take(),
  );
}

export function decodeWelcome(p: Uint8Array): Welcome5 {
  const c = new Cursor(p);
  const f = {
    lineage: c.u32(),
    generation: c.u32(),
    sub: c.u32(),
    epoch: c.u32(),
    seq: c.i64(),
    tick: c.u64(),
    hz: c.u32(),
    role: c.u8(),
    content: c.u64(),
    chunk: new TextDecoder().decode(c.bytes(c.u8())),
  };
  c.done();
  return f;
}

export interface Tick5 {
  tick: bigint;
  firstSeq: bigint;
  records: EventRecord[];
}

/** Frames a tick batch around already-encoded records. */
export function encodeTick(tick: bigint, firstSeq: bigint, records: Uint8Array[]): Uint8Array {
  const b = new Builder().u64(tick).u64(firstSeq).u16(records.length);
  for (const r of records) b.bytes(r);
  return frame(Frame5.tick, b.take());
}

export function decodeTick(p: Uint8Array): Tick5 {
  const c = new Cursor(p.subarray(0, TICK_HEADER));
  const tick = c.u64();
  const firstSeq = c.i64();
  const count = c.u16();
  return { tick, firstSeq, records: decodeRecords(p.subarray(TICK_HEADER), count) };
}

export interface Reject5 {
  intent: bigint;
  reason: number;
}

export function encodeReject(f: Reject5): Uint8Array {
  return frame(Frame5.reject, new Builder().u64(f.intent).u32(f.reason).take());
}

export function decodeReject(p: Uint8Array): Reject5 {
  const c = new Cursor(p);
  const f = { intent: c.u64(), reason: c.u32() };
  c.done();
  return f;
}

export interface Snapshot5 {
  lineage: number;
  seq: bigint;
  tick: bigint;
  epoch: number;
  wh: bigint;
  content: bigint;
  z: Uint8Array;
}

export function encodeSnapshot(f: Snapshot5): Uint8Array {
  return frame(
    Frame5.snapshot,
    new Builder()
      .u32(f.lineage)
      .u64(f.seq)
      .u64(f.tick)
      .u32(f.epoch)
      .u64(f.wh)
      .u64(f.content)
      .u32(f.z.length)
      .bytes(f.z)
      .take(),
  );
}

export function decodeSnapshot(p: Uint8Array): Snapshot5 {
  const c = new Cursor(p);
  const head = {
    lineage: c.u32(),
    seq: c.i64(),
    tick: c.u64(),
    epoch: c.u32(),
    wh: c.u64(),
    content: c.u64(),
  };
  const f = { ...head, z: c.bytes(c.u32()) };
  c.done();
  return f;
}

export interface Check5 {
  sub: number;
  tick: bigint;
  wh: bigint;
  ctMs: bigint;
}

export function encodeCheck(f: Check5): Uint8Array {
  return new Builder().u8(Datagram5.check).u32(f.sub).u64(f.tick).u64(f.wh).u64(f.ctMs).take();
}

export function decodeCheck(b: Uint8Array): Check5 {
  if (b.length !== CHECK_BYTES || b[0] !== Datagram5.check) fail("check: bad layout");
  const c = new Cursor(b.subarray(1));
  const f = { sub: c.u32(), tick: c.u64(), wh: c.u64(), ctMs: c.u64() };
  c.done();
  return f;
}

export interface Verdict5 {
  sub: number;
  lineage: number;
  tick: bigint;
  now: bigint;
  ctMs: bigint;
  flags: number;
  /** Module sha256 prefixes, verbatim wire bytes in display order. */
  cw: Uint8Array;
  pw: Uint8Array;
}

export function encodeVerdict(f: Verdict5): Uint8Array {
  if (f.cw.length !== 4 || f.pw.length !== 4) fail("verdict: module words are 4 bytes");
  return new Builder()
    .u8(Datagram5.verdict)
    .u32(f.sub)
    .u32(f.lineage)
    .u64(f.tick)
    .u64(f.now)
    .u64(f.ctMs)
    .u8(f.flags)
    .bytes(f.cw)
    .bytes(f.pw)
    .take();
}

export function decodeVerdict(b: Uint8Array): Verdict5 {
  if (b.length !== VERDICT_BYTES || b[0] !== Datagram5.verdict) fail("verdict: bad layout");
  const c = new Cursor(b.subarray(1));
  const f = {
    sub: c.u32(),
    lineage: c.u32(),
    tick: c.u64(),
    now: c.u64(),
    ctMs: c.u64(),
    flags: c.u8(),
    cw: Uint8Array.from(c.bytes(4)),
    pw: Uint8Array.from(c.bytes(4)),
  };
  c.done();
  return f;
}

// ---------- identity helpers (protocol-neutral, shared with the HUD) ----------

/** Roles as they ride the welcome frame. */
export const Role = {
  spectator: 0,
  player: 1,
} as const;

export type RoleName = keyof typeof Role;

/** Renders a u64 blob/hash id the way the blob route and the HUD spell it. */
export function hex64(v: bigint): string {
  return BigInt.asUintN(64, v).toString(16).padStart(16, "0");
}

/** The display string for a module: the wire bytes hexed left-to-right. */
export function moduleHex(bytes: Uint8Array): string {
  return [...bytes].map((n) => n.toString(16).padStart(2, "0")).join("");
}

/** The u32 the session ABI wants for a module word: the little-endian load of those bytes. */
export function moduleWord(bytes: Uint8Array): number {
  if (bytes.length < 4) {
    throw new RangeError(`module word: need 4 bytes, have ${bytes.length}`);
  }
  return new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength).getUint32(0, true);
}

/** Formats a module word that came back from the ABI, by re-storing it little-endian. */
export function moduleWordHex(word: number): string {
  const out = new Uint8Array(4);
  new DataView(out.buffer).setUint32(0, word >>> 0, true);
  return moduleHex(out);
}

/** The on-wire byte length of a varint carrying `v`. */
export function varintLen(v: number): number {
  return encodeVarint(v).length;
}

// ---------- typed frames and the streaming decoder ----------

export type ServerFrame =
  | { kind: "welcome"; value: Welcome5 }
  | { kind: "tick"; value: Tick5 }
  | { kind: "reject"; value: Reject5 }
  | { kind: "snapshot"; value: Snapshot5 };

export type ClientFrame =
  | { kind: "hello"; value: Hello5 }
  | { kind: "intent"; value: EventRecord }
  | { kind: "resync"; value: Resync5 };

/**
 * Decodes one frame body — `kind` byte already stripped — into a typed
 * server frame. Unknown kinds throw: a server that speaks a kind we do
 * not know is a version mismatch, and proto 5 is a hard cutover.
 */
export function decodeServerFrame(kind: number, body: Uint8Array): ServerFrame {
  switch (kind) {
    case Frame5.welcome:
      return { kind: "welcome", value: decodeWelcome(body) };
    case Frame5.tick:
      return { kind: "tick", value: decodeTick(body) };
    case Frame5.reject:
      return { kind: "reject", value: decodeReject(body) };
    case Frame5.snapshot:
      return { kind: "snapshot", value: decodeSnapshot(body) };
    default:
      throw new Wire5Error(`unknown server frame kind ${kind}`);
  }
}

/** The client-side mirror of `decodeServerFrame`, for harnesses standing in for the server. */
export function decodeClientFrame(kind: number, body: Uint8Array): ClientFrame {
  switch (kind) {
    case Frame5.hello:
      return { kind: "hello", value: decodeHello(body) };
    case Frame5.intent:
      return { kind: "intent", value: decodeIntent(body) };
    case Frame5.resync:
      return { kind: "resync", value: decodeResync(body) };
    default:
      throw new Wire5Error(`unknown client frame kind ${kind}`);
  }
}

export interface RawFrame {
  readonly kind: number;
  readonly body: Uint8Array;
}

/**
 * Reassembles frames from arbitrary read boundaries. `maxFrame` bounds a
 * single frame; a length prefix beyond it is a protocol violation rather
 * than a reason to buffer unboundedly. The default sits deliberately
 * above the shared MAX_FRAME so harness-side tolerance never masks a
 * producer bug — the strict cap is the session core's job.
 */
export class FrameDecoder {
  #buf = new Uint8Array(0);
  readonly #maxFrame: number;

  constructor(maxFrame = 8 << 20) {
    this.#maxFrame = maxFrame;
  }

  /** Bytes held back waiting for the rest of their frame. */
  get buffered(): number {
    return this.#buf.length;
  }

  /** Appends a read and returns every frame it completed, in order. */
  push(chunk: Uint8Array): RawFrame[] {
    if (this.#buf.length === 0) {
      this.#buf = chunk.slice();
    } else {
      const merged = new Uint8Array(this.#buf.length + chunk.length);
      merged.set(this.#buf, 0);
      merged.set(chunk, this.#buf.length);
      this.#buf = merged;
    }
    const out: RawFrame[] = [];
    let off = 0;
    for (;;) {
      const header = decodeVarint(this.#buf, off);
      if (header === null) break;
      const bodyLen = header.value;
      if (bodyLen === 0) throw new Wire5Error("zero-length frame");
      if (bodyLen > this.#maxFrame) {
        throw new Wire5Error(`frame length ${bodyLen} exceeds ${this.#maxFrame}`);
      }
      const end = off + header.length + bodyLen;
      if (end > this.#buf.length) break;
      out.push({
        kind: this.#buf[off + header.length] ?? fail("frame: no kind byte"),
        body: this.#buf.slice(off + header.length + 1, end),
      });
      off = end;
    }
    this.#buf = off === 0 ? this.#buf : this.#buf.slice(off);
    return out;
  }

  /** Drops buffered bytes. Used when a connection is torn down and redialed. */
  reset(): void {
    this.#buf = new Uint8Array(0);
  }
}

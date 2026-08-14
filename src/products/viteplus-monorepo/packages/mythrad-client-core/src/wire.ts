// Wake Up Mythra wire protocol v4: length-prefixed binary frames on one
// bidirectional WebTransport stream, plus unframed check/verdict
// datagrams. Every scalar is little-endian; the only big-endian quantity
// is the QUIC variable-length integer carrying a frame's length, which
// follows RFC 9000 §16 so the framing is the same one QUIC itself uses.
//
// This module is pure bytes in, values out. It holds no session state and
// never talks to a transport — the session core (Rust, in client.wasm)
// owns protocol behavior. The host uses this codec for its own framing
// concerns (test harnesses, netsim, diagnostics) and to keep one
// executable definition of the layouts that the Go server mirrors.

export const PROTO_VERSION = 4;

/** Client→server stream frame kinds. */
export const FrameKind = {
  hello: 1,
  intent: 2,
  resync: 3,
} as const;

/** Server→client stream frame kinds. */
export const ServerFrameKind = {
  welcome: 16,
  event: 17,
  reject: 18,
  snapshot: 19,
} as const;

/** Datagram kinds. Datagram boundaries are frame boundaries: no length prefix. */
export const DatagramKind = {
  check: 1,
  verdict: 2,
} as const;

/** Roles as they ride the welcome frame. */
export const Role = {
  spectator: 0,
  player: 1,
} as const;

export type RoleName = keyof typeof Role;

// ---------------------------------------------------------------------------
// QUIC variable-length integers (RFC 9000 §16)
// ---------------------------------------------------------------------------

/** Largest value a QUIC varint can carry: 2^62 - 1. */
export const VARINT_MAX = (1n << 62n) - 1n;

/**
 * Byte length of the varint that would encode `v`, using the shortest
 * encoding. RFC 9000 permits longer forms, so the decoder accepts them,
 * but the encoder is canonical — golden vectors depend on it.
 */
export function varintLen(v: number | bigint): 1 | 2 | 4 | 8 {
  const n = BigInt(v);
  if (n < 0n || n > VARINT_MAX) throw new RangeError(`varint out of range: ${n}`);
  if (n < 1n << 6n) return 1;
  if (n < 1n << 14n) return 2;
  if (n < 1n << 30n) return 4;
  return 8;
}

/** Writes the shortest-form varint for `v` at `off`, returning the next offset. */
export function writeVarint(dv: DataView, off: number, v: number | bigint): number {
  const n = BigInt(v);
  const len = varintLen(n);
  switch (len) {
    case 1:
      dv.setUint8(off, Number(n));
      return off + 1;
    case 2:
      dv.setUint16(off, Number(n) | 0x4000);
      return off + 2;
    case 4:
      dv.setUint32(off, Number(n) | 0x8000_0000);
      return off + 4;
    case 8:
      dv.setBigUint64(off, n | (3n << 62n));
      return off + 8;
  }
}

/**
 * Reads a varint at `off`. Returns null when fewer than the encoded number
 * of bytes are available, so an incremental reader can wait for more.
 */
export function readVarint(dv: DataView, off: number): { value: bigint; next: number } | null {
  if (off >= dv.byteLength) return null;
  const first = dv.getUint8(off);
  const len = 1 << (first >> 6);
  if (off + len > dv.byteLength) return null;
  switch (len) {
    case 1:
      return { value: BigInt(first & 0x3f), next: off + 1 };
    case 2:
      return { value: BigInt(dv.getUint16(off) & 0x3fff), next: off + 2 };
    case 4:
      return { value: BigInt(dv.getUint32(off) & 0x3fff_ffff), next: off + 4 };
    default:
      return { value: dv.getBigUint64(off) & ((1n << 62n) - 1n), next: off + 8 };
  }
}

// ---------------------------------------------------------------------------
// Frame payloads
// ---------------------------------------------------------------------------

export type Hello = {
  readonly proto: number;
  readonly sinceSeq: bigint;
  readonly sinceTick: bigint;
  readonly ticket: Uint8Array;
};

export type Intent = {
  readonly id: bigint;
  readonly kind: number;
  readonly p: Uint8Array;
};

export type Resync = {
  readonly haveSeq: bigint;
};

export type Welcome = {
  readonly epoch: number;
  readonly seq: bigint;
  readonly tick: bigint;
  readonly hz: number;
  readonly role: number;
  readonly terrain: bigint;
  readonly park: string;
};

export type ParkEvent = {
  readonly seq: bigint;
  readonly tick: bigint;
  readonly kind: number;
  readonly intent: bigint;
  readonly p: Uint8Array;
};

export type Reject = {
  readonly intent: bigint;
  readonly reason: number;
};

export type Snapshot = {
  readonly seq: bigint;
  readonly tick: bigint;
  readonly epoch: number;
  readonly wh: bigint;
  readonly terrain: bigint;
  /** Raw-deflate bytes of the park's MYP3 state. */
  readonly z: Uint8Array;
};

export type ServerFrame =
  | { readonly kind: "welcome"; readonly value: Welcome }
  | { readonly kind: "event"; readonly value: ParkEvent }
  | { readonly kind: "reject"; readonly value: Reject }
  | { readonly kind: "snapshot"; readonly value: Snapshot };

export type ClientFrame =
  | { readonly kind: "hello"; readonly value: Hello }
  | { readonly kind: "intent"; readonly value: Intent }
  | { readonly kind: "resync"; readonly value: Resync };

export type Check = {
  readonly tick: bigint;
  readonly wh: bigint;
  readonly ctMs: bigint;
};

export type Verdict = {
  readonly tick: bigint;
  readonly now: bigint;
  readonly ctMs: bigint;
  /** The tick was still in the server's hash ring. */
  readonly known: boolean;
  /** Hashes agreed. Only meaningful when `known`. */
  readonly ok: boolean;
  /** The client module's sha256, first 4 bytes, verbatim. Opaque: no endianness applies. */
  readonly cw: Uint8Array;
  /** The park module's sha256, first 4 bytes, verbatim. */
  readonly pw: Uint8Array;
};

export const VERDICT_KNOWN = 1;
export const VERDICT_OK = 2;

/** Wire size of a check datagram. */
export const CHECK_BYTES = 25;
// The design doc annotates verdict as 38 B; its own field list sums to 34
// (1 + 8 + 8 + 8 + 1 + 4 + 4). The field list is what encodes and decodes,
// so 34 is the size on the wire and the annotation is a slip.
/** Wire size of a verdict datagram. */
export const VERDICT_BYTES = 34;

// ---------------------------------------------------------------------------
// Encoding
// ---------------------------------------------------------------------------

/** A malformed frame or datagram. Always fatal for the connection carrying it. */
export class WireError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "WireError";
  }
}

function module4(b: Uint8Array, what: string): Uint8Array {
  if (b.length !== 4) throw new WireError(`${what} is ${b.length} bytes, want 4`);
  return b;
}

/** Wraps `kind`+`payload` in its varint length prefix. */
function frame(kind: number, payload: Uint8Array): Uint8Array {
  const bodyLen = 1 + payload.length;
  const pre = varintLen(bodyLen);
  const out = new Uint8Array(pre + bodyLen);
  const dv = new DataView(out.buffer);
  writeVarint(dv, 0, bodyLen);
  out[pre] = kind;
  out.set(payload, pre + 1);
  return out;
}

export function encodeHello(h: Hello): Uint8Array {
  const p = new Uint8Array(20 + h.ticket.length);
  const dv = new DataView(p.buffer);
  dv.setUint16(0, h.proto, true);
  dv.setBigInt64(2, h.sinceSeq, true);
  dv.setBigUint64(10, h.sinceTick, true);
  dv.setUint16(18, h.ticket.length, true);
  p.set(h.ticket, 20);
  return frame(FrameKind.hello, p);
}

export function encodeIntent(i: Intent): Uint8Array {
  const p = new Uint8Array(12 + i.p.length);
  const dv = new DataView(p.buffer);
  dv.setBigUint64(0, i.id, true);
  dv.setUint16(8, i.kind, true);
  dv.setUint16(10, i.p.length, true);
  p.set(i.p, 12);
  return frame(FrameKind.intent, p);
}

export function encodeResync(r: Resync): Uint8Array {
  const p = new Uint8Array(8);
  new DataView(p.buffer).setBigInt64(0, r.haveSeq, true);
  return frame(FrameKind.resync, p);
}

export function encodeWelcome(w: Welcome): Uint8Array {
  const park = new TextEncoder().encode(w.park);
  const p = new Uint8Array(34 + park.length);
  const dv = new DataView(p.buffer);
  dv.setUint32(0, w.epoch, true);
  dv.setBigInt64(4, w.seq, true);
  dv.setBigUint64(12, w.tick, true);
  dv.setUint32(20, w.hz, true);
  dv.setUint8(24, w.role);
  dv.setBigUint64(25, w.terrain, true);
  dv.setUint8(33, park.length);
  p.set(park, 34);
  return frame(ServerFrameKind.welcome, p);
}

export function encodeEvent(e: ParkEvent): Uint8Array {
  const p = new Uint8Array(28 + e.p.length);
  const dv = new DataView(p.buffer);
  dv.setBigInt64(0, e.seq, true);
  dv.setBigUint64(8, e.tick, true);
  dv.setUint16(16, e.kind, true);
  dv.setBigUint64(18, e.intent, true);
  dv.setUint16(26, e.p.length, true);
  p.set(e.p, 28);
  return frame(ServerFrameKind.event, p);
}

export function encodeReject(r: Reject): Uint8Array {
  const p = new Uint8Array(12);
  const dv = new DataView(p.buffer);
  dv.setBigUint64(0, r.intent, true);
  dv.setUint32(8, r.reason, true);
  return frame(ServerFrameKind.reject, p);
}

export function encodeSnapshot(s: Snapshot): Uint8Array {
  const p = new Uint8Array(40 + s.z.length);
  const dv = new DataView(p.buffer);
  dv.setBigInt64(0, s.seq, true);
  dv.setBigUint64(8, s.tick, true);
  dv.setUint32(16, s.epoch, true);
  dv.setBigUint64(20, s.wh, true);
  dv.setBigUint64(28, s.terrain, true);
  dv.setUint32(36, s.z.length, true);
  p.set(s.z, 40);
  return frame(ServerFrameKind.snapshot, p);
}

export function encodeCheck(c: Check): Uint8Array {
  const out = new Uint8Array(CHECK_BYTES);
  const dv = new DataView(out.buffer);
  dv.setUint8(0, DatagramKind.check);
  dv.setBigUint64(1, c.tick, true);
  dv.setBigUint64(9, c.wh, true);
  dv.setBigUint64(17, c.ctMs, true);
  return out;
}

export function encodeVerdict(v: Verdict): Uint8Array {
  const out = new Uint8Array(VERDICT_BYTES);
  const dv = new DataView(out.buffer);
  dv.setUint8(0, DatagramKind.verdict);
  dv.setBigUint64(1, v.tick, true);
  dv.setBigUint64(9, v.now, true);
  dv.setBigUint64(17, v.ctMs, true);
  dv.setUint8(25, (v.known ? VERDICT_KNOWN : 0) | (v.known && v.ok ? VERDICT_OK : 0));
  out.set(module4(v.cw, "verdict cw"), 26);
  out.set(module4(v.pw, "verdict pw"), 30);
  return out;
}

// ---------------------------------------------------------------------------
// Decoding
// ---------------------------------------------------------------------------

function view(b: Uint8Array): DataView {
  return new DataView(b.buffer, b.byteOffset, b.byteLength);
}

function need(b: Uint8Array, n: number, what: string): void {
  if (b.length < n) throw new WireError(`${what}: need ${n} bytes, have ${b.length}`);
}

/**
 * A frame body must be exactly as long as its own fields declare. The
 * length the varint promised and the bytes actually written are two
 * different things, and a mismatch between them is precisely the encoder
 * bug this catches — trailing bytes are a malformed frame, not padding.
 */
function exact(b: Uint8Array, n: number, what: string): void {
  if (b.length !== n) throw new WireError(`${what}: body is ${b.length} bytes, want ${n}`);
}

/**
 * Decodes one frame body — `kind` byte already stripped — into a typed
 * server frame. Unknown kinds throw: a server that speaks a kind we do not
 * know is a version mismatch, and proto 4 is a hard cutover.
 */
export function decodeServerFrame(kind: number, body: Uint8Array): ServerFrame {
  switch (kind) {
    case ServerFrameKind.welcome:
      return { kind: "welcome", value: decodeWelcome(body) };
    case ServerFrameKind.event:
      return { kind: "event", value: decodeEvent(body) };
    case ServerFrameKind.reject:
      return { kind: "reject", value: decodeReject(body) };
    case ServerFrameKind.snapshot:
      return { kind: "snapshot", value: decodeSnapshot(body) };
    default:
      throw new WireError(`unknown server frame kind ${kind}`);
  }
}

/** The client-side mirror of `decodeServerFrame`, for harnesses standing in for the server. */
export function decodeClientFrame(kind: number, body: Uint8Array): ClientFrame {
  switch (kind) {
    case FrameKind.hello:
      return { kind: "hello", value: decodeHello(body) };
    case FrameKind.intent:
      return { kind: "intent", value: decodeIntent(body) };
    case FrameKind.resync:
      return { kind: "resync", value: decodeResync(body) };
    default:
      throw new WireError(`unknown client frame kind ${kind}`);
  }
}

export function decodeHello(b: Uint8Array): Hello {
  need(b, 20, "hello");
  const dv = view(b);
  const ticketLen = dv.getUint16(18, true);
  exact(b, 20 + ticketLen, "hello");
  return {
    proto: dv.getUint16(0, true),
    sinceSeq: dv.getBigInt64(2, true),
    sinceTick: dv.getBigUint64(10, true),
    ticket: b.slice(20, 20 + ticketLen),
  };
}

export function decodeIntent(b: Uint8Array): Intent {
  need(b, 12, "intent");
  const dv = view(b);
  const plen = dv.getUint16(10, true);
  exact(b, 12 + plen, "intent");
  return {
    id: dv.getBigUint64(0, true),
    kind: dv.getUint16(8, true),
    p: b.slice(12, 12 + plen),
  };
}

export function decodeResync(b: Uint8Array): Resync {
  exact(b, 8, "resync");
  return { haveSeq: view(b).getBigInt64(0, true) };
}

export function decodeWelcome(b: Uint8Array): Welcome {
  need(b, 34, "welcome");
  const dv = view(b);
  const parkLen = dv.getUint8(33);
  exact(b, 34 + parkLen, "welcome");
  return {
    epoch: dv.getUint32(0, true),
    seq: dv.getBigInt64(4, true),
    tick: dv.getBigUint64(12, true),
    hz: dv.getUint32(20, true),
    role: dv.getUint8(24),
    terrain: dv.getBigUint64(25, true),
    park: new TextDecoder().decode(b.subarray(34, 34 + parkLen)),
  };
}

export function decodeEvent(b: Uint8Array): ParkEvent {
  need(b, 28, "event");
  const dv = view(b);
  const plen = dv.getUint16(26, true);
  exact(b, 28 + plen, "event");
  return {
    seq: dv.getBigInt64(0, true),
    tick: dv.getBigUint64(8, true),
    kind: dv.getUint16(16, true),
    intent: dv.getBigUint64(18, true),
    p: b.slice(28, 28 + plen),
  };
}

export function decodeReject(b: Uint8Array): Reject {
  exact(b, 12, "reject");
  const dv = view(b);
  return { intent: dv.getBigUint64(0, true), reason: dv.getUint32(8, true) };
}

export function decodeSnapshot(b: Uint8Array): Snapshot {
  need(b, 40, "snapshot");
  const dv = view(b);
  const zlen = dv.getUint32(36, true);
  exact(b, 40 + zlen, "snapshot");
  return {
    seq: dv.getBigInt64(0, true),
    tick: dv.getBigUint64(8, true),
    epoch: dv.getUint32(16, true),
    wh: dv.getBigUint64(20, true),
    terrain: dv.getBigUint64(28, true),
    z: b.slice(40, 40 + zlen),
  };
}

export function decodeCheck(b: Uint8Array): Check {
  if (b.length !== CHECK_BYTES) {
    throw new WireError(`check datagram is ${b.length}B, want ${CHECK_BYTES}`);
  }
  const dv = view(b);
  if (dv.getUint8(0) !== DatagramKind.check) {
    throw new WireError(`datagram kind ${dv.getUint8(0)} is not a check`);
  }
  return {
    tick: dv.getBigUint64(1, true),
    wh: dv.getBigUint64(9, true),
    ctMs: dv.getBigUint64(17, true),
  };
}

export function decodeVerdict(b: Uint8Array): Verdict {
  if (b.length !== VERDICT_BYTES) {
    throw new WireError(`verdict datagram is ${b.length}B, want ${VERDICT_BYTES}`);
  }
  const dv = view(b);
  if (dv.getUint8(0) !== DatagramKind.verdict) {
    throw new WireError(`datagram kind ${dv.getUint8(0)} is not a verdict`);
  }
  const flags = dv.getUint8(25);
  const known = (flags & VERDICT_KNOWN) !== 0;
  return {
    tick: dv.getBigUint64(1, true),
    now: dv.getBigUint64(9, true),
    ctMs: dv.getBigUint64(17, true),
    known,
    ok: known && (flags & VERDICT_OK) !== 0,
    cw: b.slice(26, 30),
    pw: b.slice(30, 34),
  };
}

// ---------------------------------------------------------------------------
// Incremental stream decoding
// ---------------------------------------------------------------------------

/** A frame lifted off the stream: its kind byte and the bytes after it. */
export type RawFrame = { readonly kind: number; readonly body: Uint8Array };

/**
 * Reassembles frames from arbitrarily-chopped stream reads. WebTransport
 * hands the reader whatever bytes have arrived, so a frame may span reads
 * and a read may carry several frames plus half of another.
 *
 * `maxFrame` bounds a single frame; a length prefix beyond it is a
 * protocol violation rather than a reason to buffer unboundedly. Nothing
 * is ever pre-allocated to a declared length — only bytes that actually
 * arrived are held — so a hostile prefix costs the varint and nothing
 * more.
 *
 * The default is deliberately far above the server's own read cap, and
 * the asymmetry is correct: do not harmonize the two downward. The park
 * module's IO_CAP is 64 KiB, so an uncompressed snapshot cannot exceed
 * that, and a snapshot frame is 40 bytes plus the deflate of a state that
 * size — a few bytes over 64 KiB even in the incompressible worst case.
 * What matters is that a client-side read cap stays comfortably above
 * ~64 KiB. If IO_CAP ever grows, this ceiling and the server's must both
 * be revisited against the new number.
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
    const dv = view(this.#buf);
    for (;;) {
      const header = readVarint(dv, off);
      if (header === null) break;
      const bodyLen = header.value;
      if (bodyLen === 0n) throw new WireError("zero-length frame");
      if (bodyLen > BigInt(this.#maxFrame)) {
        throw new WireError(`frame length ${bodyLen} exceeds ${this.#maxFrame}`);
      }
      const len = Number(bodyLen);
      const end = header.next + len;
      if (end > this.#buf.length) break;
      out.push({
        kind: this.#buf[header.next]!,
        body: this.#buf.slice(header.next + 1, end),
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

/** Renders a u64 blob/hash id the way `/terrain/<hex>` and the HUD spell it. */
export function hex64(v: bigint): string {
  return BigInt.asUintN(64, v).toString(16).padStart(16, "0");
}

/**
 * The `/wt-info` display string for a module: the wire bytes hexed
 * left-to-right. `cw`/`pw` are opaque bytes, so this is a straight
 * transcription — no endianness is involved.
 */
export function moduleHex(bytes: Uint8Array): string {
  return [...bytes].map((n) => n.toString(16).padStart(2, "0")).join("");
}

/**
 * The u32 the session ABI wants for a module word (`session_module_swapped`,
 * `request(2, pw)`): the little-endian load of those same four bytes.
 */
export function moduleWord(bytes: Uint8Array): number {
  need(bytes, 4, "module word");
  return view(bytes).getUint32(0, true);
}

/** Formats a module word that came back from the ABI, by re-storing it little-endian. */
export function moduleWordHex(word: number): string {
  const out = new Uint8Array(4);
  new DataView(out.buffer).setUint32(0, word >>> 0, true);
  return moduleHex(out);
}

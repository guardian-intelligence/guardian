// Golden vectors are written out byte by byte, derived by hand from the
// layouts in the design doc rather than from this codec's own output. A
// round-trip test only proves the encoder and decoder agree with each
// other; these prove they agree with the Go server.

import { describe, expect, it } from "vitest";
import {
  CHECK_BYTES,
  DatagramKind,
  FrameKind,
  PROTO_VERSION,
  Role,
  ServerFrameKind,
  VARINT_MAX,
  VERDICT_BYTES,
  WireError,
  decodeCheck,
  decodeClientFrame,
  decodeEvent,
  decodeHello,
  decodeIntent,
  decodeReject,
  decodeResync,
  decodeServerFrame,
  decodeSnapshot,
  decodeVerdict,
  decodeWelcome,
  encodeCheck,
  encodeEvent,
  encodeHello,
  encodeIntent,
  encodeReject,
  encodeResync,
  encodeSnapshot,
  encodeVerdict,
  encodeWelcome,
  moduleHex,
  moduleWordHex,
  hex64,
  moduleWord,
  readVarint,
  varintLen,
  writeVarint,
} from "../src/wire.ts";

/** Parses a whitespace-separated hex dump into bytes, so goldens read like a spec. */
function h(dump: string): Uint8Array {
  const parts = dump.trim().split(/\s+/);
  return Uint8Array.from(parts.map((p) => parseInt(p, 16)));
}

function dv(b: Uint8Array): DataView {
  return new DataView(b.buffer, b.byteOffset, b.byteLength);
}

/** Splits a frame into its varint prefix, kind byte, and body. */
function split(frame: Uint8Array): { kind: number; body: Uint8Array } {
  const header = readVarint(dv(frame), 0);
  if (header === null) throw new Error("truncated varint");
  const start = header.next;
  const end = start + Number(header.value);
  expect(end).toBe(frame.length);
  return { kind: frame[start]!, body: frame.slice(start + 1, end) };
}

describe("QUIC varint (RFC 9000 §16)", () => {
  // The four worked examples from RFC 9000 Appendix A.1.
  const vectors: [string, bigint][] = [
    ["c2 19 7c 5e ff 14 e8 8c", 151288809941952652n],
    ["9d 7f 3e 7d", 494878333n],
    ["7b bd", 15293n],
    ["25", 37n],
  ];

  it.each(vectors)("decodes %s", (dump, want) => {
    const b = h(dump);
    const got = readVarint(dv(b), 0);
    expect(got).not.toBeNull();
    expect(got!.value).toBe(want);
    expect(got!.next).toBe(b.length);
  });

  it.each(vectors)("re-encodes %s canonically", (dump, want) => {
    const out = new Uint8Array(8);
    const next = writeVarint(dv(out), 0, want);
    expect([...out.subarray(0, next)]).toEqual([...h(dump)]);
  });

  it("decodes the RFC's non-canonical two-byte 37", () => {
    // The RFC allows a longer encoding than necessary; we must read it
    // even though we never write it.
    const got = readVarint(dv(h("40 25")), 0);
    expect(got).toEqual({ value: 37n, next: 2 });
    expect(varintLen(37)).toBe(1);
  });

  it("picks the shortest form at every boundary", () => {
    expect(varintLen(63)).toBe(1);
    expect(varintLen(64)).toBe(2);
    expect(varintLen(16383)).toBe(2);
    expect(varintLen(16384)).toBe(4);
    expect(varintLen(2 ** 30 - 1)).toBe(4);
    expect(varintLen(2 ** 30)).toBe(8);
    expect(varintLen(VARINT_MAX)).toBe(8);
  });

  it("refuses values a varint cannot carry", () => {
    expect(() => varintLen(VARINT_MAX + 1n)).toThrow(RangeError);
    expect(() => varintLen(-1)).toThrow(RangeError);
  });

  it("returns null when the encoded length is not all there yet", () => {
    const b = h("c2 19 7c");
    expect(readVarint(dv(b), 0)).toBeNull();
    expect(readVarint(dv(h("25")), 1)).toBeNull();
  });
});

describe("client frame goldens", () => {
  it("hello", () => {
    // varint 25 | kind 1 | proto=4 | since_seq=-1 | since_tick=0 |
    // ticket_len=4 | ticket=deadbeef
    const golden = h(`
      19 01
      04 00
      ff ff ff ff ff ff ff ff
      00 00 00 00 00 00 00 00
      04 00
      de ad be ef
    `);
    const value = {
      proto: PROTO_VERSION,
      sinceSeq: -1n,
      sinceTick: 0n,
      ticket: h("de ad be ef"),
    };
    expect([...encodeHello(value)]).toEqual([...golden]);
    const { kind, body } = split(golden);
    expect(kind).toBe(FrameKind.hello);
    expect(decodeHello(body)).toEqual(value);
  });

  it("intent: move_to for dog 1122334455667788 to node 300", () => {
    // varint 23 | kind 2 | id=0x0000000100000002 | kind=4 | plen=10 |
    // p = dog u64 || node u16
    const golden = h(`
      17 02
      02 00 00 00 01 00 00 00
      04 00
      0a 00
      88 77 66 55 44 33 22 11
      2c 01
    `);
    const value = {
      id: 0x0000_0001_0000_0002n,
      kind: 4,
      p: h("88 77 66 55 44 33 22 11 2c 01"),
    };
    expect([...encodeIntent(value)]).toEqual([...golden]);
    const { kind, body } = split(golden);
    expect(kind).toBe(FrameKind.intent);
    expect(decodeIntent(body)).toEqual(value);
  });

  it("resync", () => {
    const golden = h("09 03 2a 00 00 00 00 00 00 00");
    expect([...encodeResync({ haveSeq: 42n })]).toEqual([...golden]);
    const { kind, body } = split(golden);
    expect(kind).toBe(FrameKind.resync);
    expect(decodeResync(body)).toEqual({ haveSeq: 42n });
  });

  it("an empty intent payload still carries its plen", () => {
    const golden = h("0d 02 01 00 00 00 00 00 00 00 03 00 00 00");
    expect([...encodeIntent({ id: 1n, kind: 3, p: new Uint8Array(0) })]).toEqual([...golden]);
  });
});

describe("server frame goldens", () => {
  it("welcome", () => {
    // varint 46 | kind 16 | epoch=7 | seq=100 | tick=2400 | hz=24 |
    // role=1 (player) | terrain=0x0123456789abcdef | park_len=11 |
    // "park-mythra"
    const golden = h(`
      2e 10
      07 00 00 00
      64 00 00 00 00 00 00 00
      60 09 00 00 00 00 00 00
      18 00 00 00
      01
      ef cd ab 89 67 45 23 01
      0b
      70 61 72 6b 2d 6d 79 74 68 72 61
    `);
    const value = {
      epoch: 7,
      seq: 100n,
      tick: 2400n,
      hz: 24,
      role: Role.player,
      terrain: 0x0123_4567_89ab_cdefn,
      park: "park-mythra",
    };
    expect([...encodeWelcome(value)]).toEqual([...golden]);
    const { kind, body } = split(golden);
    expect(kind).toBe(ServerFrameKind.welcome);
    expect(decodeWelcome(body)).toEqual(value);
    expect(decodeServerFrame(kind, body)).toEqual({ kind: "welcome", value });
  });

  it("event: boost_set at seq 101", () => {
    // varint 38 | kind 17 | seq=101 | tick=2401 | kind=8 |
    // intent=0x0000000100000002 | plen=9 | p = dog u64 || on
    const golden = h(`
      26 11
      65 00 00 00 00 00 00 00
      61 09 00 00 00 00 00 00
      08 00
      02 00 00 00 01 00 00 00
      09 00
      88 77 66 55 44 33 22 11 01
    `);
    const value = {
      seq: 101n,
      tick: 2401n,
      kind: 8,
      intent: 0x0000_0001_0000_0002n,
      p: h("88 77 66 55 44 33 22 11 01"),
    };
    expect([...encodeEvent(value)]).toEqual([...golden]);
    const { kind, body } = split(golden);
    expect(kind).toBe(ServerFrameKind.event);
    expect(decodeEvent(body)).toEqual(value);
  });

  it("reject", () => {
    // varint 13 | kind 18 | intent | reason=3 (ERR_ABSENT)
    const golden = h("0d 12 02 00 00 00 01 00 00 00 03 00 00 00");
    const value = { intent: 0x0000_0001_0000_0002n, reason: 3 };
    expect([...encodeReject(value)]).toEqual([...golden]);
    const { kind, body } = split(golden);
    expect(kind).toBe(ServerFrameKind.reject);
    expect(decodeReject(body)).toEqual(value);
  });

  it("snapshot", () => {
    // varint 44 | kind 19 | seq=100 | tick=2400 | epoch=7 |
    // wh=0xfeedfacecafebeef | terrain=0x0123456789abcdef | zlen=3 | z
    const golden = h(`
      2c 13
      64 00 00 00 00 00 00 00
      60 09 00 00 00 00 00 00
      07 00 00 00
      ef be fe ca ce fa ed fe
      ef cd ab 89 67 45 23 01
      03 00 00 00
      01 02 03
    `);
    const value = {
      seq: 100n,
      tick: 2400n,
      epoch: 7,
      wh: 0xfeed_face_cafe_beefn,
      terrain: 0x0123_4567_89ab_cdefn,
      z: h("01 02 03"),
    };
    expect([...encodeSnapshot(value)]).toEqual([...golden]);
    const { kind, body } = split(golden);
    expect(kind).toBe(ServerFrameKind.snapshot);
    expect(decodeSnapshot(body)).toEqual(value);
  });

  it("a payload past 63 bytes takes a two-byte varint prefix", () => {
    // 40 + 24 = 64 bytes of payload, +1 kind = 65, which no longer fits
    // the 6-bit form: 0x4041 is 65 with the two-byte tag.
    const frame = encodeSnapshot({
      seq: 0n,
      tick: 0n,
      epoch: 0,
      wh: 0n,
      terrain: 0n,
      z: new Uint8Array(24),
    });
    expect(frame.length).toBe(2 + 65);
    expect([...frame.subarray(0, 2)]).toEqual([0x40, 0x41]);
  });
});

describe("datagram goldens", () => {
  it("check", () => {
    // kind 1 | tick=2400 | wh=0xfeedfacecafebeef | ct_ms=1000000
    const golden = h(`
      01
      60 09 00 00 00 00 00 00
      ef be fe ca ce fa ed fe
      40 42 0f 00 00 00 00 00
    `);
    expect(golden.length).toBe(CHECK_BYTES);
    const value = { tick: 2400n, wh: 0xfeed_face_cafe_beefn, ctMs: 1_000_000n };
    expect([...encodeCheck(value)]).toEqual([...golden]);
    expect(decodeCheck(golden)).toEqual(value);
    expect(golden[0]).toBe(DatagramKind.check);
  });

  it("verdict: known and ok", () => {
    // kind 2 | tick=2400 | now=1000050 | ct_ms=1000000 | flags=3 |
    // cw=0x11223344 | pw=0xaabbccdd
    const golden = h(`
      02
      60 09 00 00 00 00 00 00
      72 42 0f 00 00 00 00 00
      40 42 0f 00 00 00 00 00
      03
      44 33 22 11
      dd cc bb aa
    `);
    // The design doc annotates verdict as 38 B; its own field list sums to
    // 34, and the field list is what encodes and decodes.
    expect(golden.length).toBe(VERDICT_BYTES);
    expect(VERDICT_BYTES).toBe(34);
    const value = {
      tick: 2400n,
      now: 1_000_050n,
      ctMs: 1_000_000n,
      known: true,
      ok: true,
      cw: h("44 33 22 11"),
      pw: h("dd cc bb aa"),
    };
    expect([...encodeVerdict(value)]).toEqual([...golden]);
    expect(decodeVerdict(golden)).toEqual(value);
  });

  it("an unknown tick reports not-ok regardless of the ok bit", () => {
    // flags 0: neither known nor ok. A server that sets ok without known
    // is still reported as unknown — `ok` is only defined when `known`.
    const raw = encodeVerdict({
      tick: 1n,
      now: 2n,
      ctMs: 3n,
      known: false,
      ok: true,
      cw: new Uint8Array(4),
      pw: new Uint8Array(4),
    });
    expect(raw[25]).toBe(0);
    const back = decodeVerdict(raw);
    expect(back.known).toBe(false);
    expect(back.ok).toBe(false);
  });

  it("a mismatch is known but not ok", () => {
    const raw = encodeVerdict({
      tick: 9n,
      now: 0n,
      ctMs: 0n,
      known: true,
      ok: false,
      cw: new Uint8Array(4),
      pw: new Uint8Array(4),
    });
    expect(raw[25]).toBe(1);
    expect(decodeVerdict(raw)).toMatchObject({ known: true, ok: false });
  });

  it("refuses a datagram of the wrong length or kind", () => {
    expect(() => decodeCheck(new Uint8Array(24))).toThrow(WireError);
    expect(() => decodeVerdict(new Uint8Array(38))).toThrow(WireError);
    const check = encodeCheck({ tick: 0n, wh: 0n, ctMs: 0n });
    expect(() => decodeVerdict(new Uint8Array(VERDICT_BYTES))).toThrow(/not a verdict/);
    check[0] = 9;
    expect(() => decodeCheck(check)).toThrow(/not a check/);
  });
});

describe("module words and blob ids", () => {
  it("carries the module bytes verbatim and loads them LE for the ABI", () => {
    // cw/pw are opaque [4]u8: /wt-info's display string is those bytes
    // hexed left-to-right, with no endianness involved.
    const wire = h("de ad be ef");
    expect(moduleHex(wire)).toBe("deadbeef");
    // Only the wasm ABI wants an integer, and that integer is the LE load.
    expect(moduleWord(wire)).toBe(0xefbe_adde);
    expect(moduleWordHex(moduleWord(wire))).toBe("deadbeef");
  });

  it("renders a terrain blob id the way /terrain/<hex> wants it", () => {
    // Unlike the module word, park.go formats this one with %016x, so the
    // nibble order is the plain big-endian rendering of the u64.
    expect(hex64(0x0123_4567_89ab_cdefn)).toBe("0123456789abcdef");
    expect(hex64(1n)).toBe("0000000000000001");
    expect(hex64(-1n)).toBe("ffffffffffffffff");
  });
});

describe("round trips", () => {
  it("survives every payload length a u16 can name", () => {
    for (const plen of [0, 1, 63, 64, 255, 256, 16383, 16384, 65535]) {
      const p = new Uint8Array(plen).map((_, i) => i & 0xff);
      const frame = encodeIntent({ id: 7n, kind: 2, p });
      const { kind, body } = split(frame);
      expect(decodeClientFrame(kind, body)).toEqual({
        kind: "intent",
        value: { id: 7n, kind: 2, p },
      });
    }
  });

  it("keeps seq signed and tick unsigned at their extremes", () => {
    const value = {
      seq: -(2n ** 63n),
      tick: 2n ** 64n - 1n,
      kind: 0xffff,
      intent: 2n ** 64n - 1n,
      p: new Uint8Array(0),
    };
    const { kind, body } = split(encodeEvent(value));
    expect(decodeServerFrame(kind, body)).toEqual({ kind: "event", value });
  });

  it("refuses a frame kind from another protocol version", () => {
    expect(() => decodeServerFrame(20, new Uint8Array(0))).toThrow(WireError);
    expect(() => decodeClientFrame(16, new Uint8Array(0))).toThrow(WireError);
  });

  it("refuses a body longer than the fields it declares", () => {
    // The other half of "declared length equals bytes written": an
    // encoder that promised one length and wrote more shows up here as a
    // frame with trailing bytes. The Go decoders reject those too, so a
    // bug on either side surfaces at the first frame instead of drifting.
    const { kind, body } = split(
      encodeEvent({
        seq: 1n,
        tick: 2n,
        kind: 3,
        intent: 4n,
        p: Uint8Array.of(5),
      }),
    );
    const padded = new Uint8Array(body.length + 1);
    padded.set(body);
    expect(() => decodeServerFrame(kind, padded)).toThrow(/body is 30 bytes, want 29/);
    expect(() => decodeReject(new Uint8Array(13))).toThrow(WireError);
    expect(() => decodeResync(new Uint8Array(9))).toThrow(WireError);
  });

  it("refuses a body that is shorter than the fields it declares", () => {
    const good = encodeWelcome({
      epoch: 1,
      seq: 1n,
      tick: 1n,
      hz: 24,
      role: Role.spectator,
      terrain: 1n,
      park: "park-mythra",
    });
    const { body } = split(good);
    expect(() => decodeWelcome(body.slice(0, body.length - 1))).toThrow(WireError);
    expect(() => decodeWelcome(body.slice(0, 20))).toThrow(WireError);
  });
});

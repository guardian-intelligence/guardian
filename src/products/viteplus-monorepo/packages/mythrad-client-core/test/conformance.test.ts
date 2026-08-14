// Invariant 1 from the design doc: every encoder/decoder pair, in every
// language, produces the same bytes. The hex strings below are copied
// VERBATIM from the pinned vector file (scratchpad/proto4-goldens.txt),
// which the Go implementation minted independently of this codec. Keep
// them unbroken and unedited — a reformatted golden is a golden nobody
// can diff against its source.
//
// The fixtures are deliberately asymmetric — 1024 for a tick, 9 for a
// seq, distinct byte patterns for the two 64-bit hashes — so a
// transposed pair of same-width fields cannot pass by coincidence.

import { describe, expect, it } from "vitest";
import {
  PROTO_VERSION,
  Role,
  decodeCheck,
  decodeClientFrame,
  decodeServerFrame,
  decodeVerdict,
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
  moduleWord,
  moduleWordHex,
  readVarint,
  varintLen,
  type ClientFrame,
  type ServerFrame,
} from "@guardian/chunkies-testkit";

function bytes(hexDump: string): Uint8Array {
  const clean = hexDump.replace(/\s+/g, "");
  const out = new Uint8Array(clean.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(clean.slice(i * 2, i * 2 + 2), 16);
  return out;
}

function hexOf(b: Uint8Array): string {
  return [...b].map((n) => n.toString(16).padStart(2, "0")).join("");
}

const TICK = 1024n;
const SEQ = 9n;
const TERRAIN = 0x0123_4567_89ab_cdefn;
const WH = 0xfeed_face_cafe_beefn;
const CT_MS = 1_700_000_000_000n;
/** Dog 0x1122334455667788 as the sim's payloads carry it: little-endian. */
const DOG = bytes("8877665544332211");

type FrameCase = {
  readonly name: string;
  /** Declared size from the vector file, including the varint prefix. */
  readonly size: number;
  readonly golden: string;
  readonly encoded: Uint8Array;
  readonly decoded: ServerFrame | ClientFrame;
};

const frames: FrameCase[] = [
  {
    name: "hello",
    size: 25,
    golden: "18010400ffffffffffffffff00000000000000000300746b74",
    encoded: encodeHello({
      proto: PROTO_VERSION,
      sinceSeq: -1n,
      sinceTick: 0n,
      ticket: new TextEncoder().encode("tkt"),
    }),
    decoded: {
      kind: "hello",
      value: {
        proto: 4,
        sinceSeq: -1n,
        sinceTick: 0n,
        ticket: new TextEncoder().encode("tkt"),
      },
    },
  },
  {
    name: "intent",
    size: 24,
    golden: "1702080706050403020104000a0088776655443322110700",
    encoded: encodeIntent({
      id: 0x0102_0304_0506_0708n,
      kind: 4,
      p: bytes("8877665544332211" + "0700"),
    }),
    decoded: {
      kind: "intent",
      value: {
        id: 0x0102_0304_0506_0708n,
        kind: 4,
        p: bytes("88776655443322110700"),
      },
    },
  },
  {
    name: "resync",
    size: 10,
    golden: "09039210000000000000",
    encoded: encodeResync({ haveSeq: 4242n }),
    decoded: { kind: "resync", value: { haveSeq: 4242n } },
  },
  {
    name: "welcome",
    size: 47,
    golden:
      "2e1001000000070000000000000000040000000000001800000001efcdab89674523010b7061726b2d6d7974687261",
    encoded: encodeWelcome({
      epoch: 1,
      seq: 7n,
      tick: TICK,
      hz: 24,
      role: Role.player,
      terrain: TERRAIN,
      park: "park-mythra",
    }),
    decoded: {
      kind: "welcome",
      value: {
        epoch: 1,
        seq: 7n,
        tick: TICK,
        hz: 24,
        role: Role.player,
        terrain: TERRAIN,
        park: "park-mythra",
      },
    },
  },
  {
    name: "event",
    size: 38,
    golden: "2511090000000000000000040000000000000300050000000000000008008877665544332211",
    encoded: encodeEvent({ seq: SEQ, tick: TICK, kind: 3, intent: 5n, p: DOG }),
    decoded: {
      kind: "event",
      value: { seq: SEQ, tick: TICK, kind: 3, intent: 5n, p: DOG },
    },
  },
  {
    name: "reject",
    size: 14,
    golden: "0d12050000000000000065000000",
    encoded: encodeReject({ intent: 5n, reason: 101 }),
    decoded: { kind: "reject", value: { intent: 5n, reason: 101 } },
  },
  {
    name: "snapshot",
    size: 46,
    golden:
      "2d130900000000000000000400000000000001000000efbefecacefaedfeefcdab896745230104000000deadbeef",
    encoded: encodeSnapshot({
      seq: SEQ,
      tick: TICK,
      epoch: 1,
      wh: WH,
      terrain: TERRAIN,
      z: bytes("deadbeef"),
    }),
    decoded: {
      kind: "snapshot",
      value: {
        seq: SEQ,
        tick: TICK,
        epoch: 1,
        wh: WH,
        terrain: TERRAIN,
        z: bytes("deadbeef"),
      },
    },
  },
];

describe("pinned proto-4 frame vectors", () => {
  it.each(frames)("$name encodes to the pinned bytes", (tc) => {
    expect(hexOf(tc.encoded)).toBe(tc.golden);
  });

  it.each(frames)("$name is the size the vector file declares", (tc) => {
    expect(tc.encoded.length).toBe(tc.size);
    expect(tc.golden.length / 2).toBe(tc.size);
  });

  // The Go side found a real 4-byte encoder bug with exactly this check:
  // a frame can declare one length in its prefix and then write another.
  it.each(frames)("$name declares the body length it actually wrote", (tc) => {
    const dv = new DataView(tc.encoded.buffer, tc.encoded.byteOffset, tc.encoded.byteLength);
    const header = readVarint(dv, 0);
    expect(header).not.toBeNull();
    const declared = Number(header!.value);
    const written = tc.encoded.length - header!.next;
    expect(declared).toBe(written);
    // And the prefix itself is the shortest form for that length.
    expect(header!.next).toBe(varintLen(declared));
  });

  it.each(frames)("$name decodes back to the value that produced it", (tc) => {
    const golden = bytes(tc.golden);
    const dv = new DataView(golden.buffer);
    const header = readVarint(dv, 0)!;
    const kind = golden[header.next]!;
    const body = golden.slice(header.next + 1);
    const decode =
      tc.decoded.kind === "hello" || tc.decoded.kind === "intent" || tc.decoded.kind === "resync"
        ? decodeClientFrame
        : decodeServerFrame;
    expect(decode(kind, body)).toEqual(tc.decoded);
  });
});

describe("pinned proto-4 datagram vectors", () => {
  it("check", () => {
    const golden = "010004000000000000efbefecacefaedfe0068e5cf8b010000";
    const raw = encodeCheck({ tick: TICK, wh: WH, ctMs: CT_MS });
    expect(hexOf(raw)).toBe(golden);
    expect(raw.length).toBe(25);
    expect(decodeCheck(bytes(golden))).toEqual({ tick: TICK, wh: WH, ctMs: CT_MS });
  });

  it("verdict, with both module words carried verbatim", () => {
    const golden = "02000400000000000006040000000000000068e5cf8b010000039abcdef012345678";
    const raw = encodeVerdict({
      tick: TICK,
      now: 1030n,
      ctMs: CT_MS,
      known: true,
      ok: true,
      cw: bytes("9abcdef0"),
      pw: bytes("12345678"),
    });
    expect(hexOf(raw)).toBe(golden);
    expect(raw.length).toBe(34);
    const back = decodeVerdict(bytes(golden));
    expect(back).toEqual({
      tick: TICK,
      now: 1030n,
      ctMs: CT_MS,
      known: true,
      ok: true,
      cw: bytes("9abcdef0"),
      pw: bytes("12345678"),
    });
    // The display string is the wire bytes read straight through.
    expect(moduleHex(back.cw)).toBe("9abcdef0");
    expect(moduleHex(back.pw)).toBe("12345678");
  });

  it("a module word round-trips through the u32 the session ABI takes", () => {
    // The same assertion the Go side makes: the display bytes loaded
    // little-endian are 0xf0debc9a, and re-storing them puts them back.
    expect(moduleWord(bytes("9abcdef0"))).toBe(0xf0de_bc9a);
    expect(moduleWordHex(0xf0de_bc9a)).toBe("9abcdef0");
  });
});

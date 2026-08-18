// WebTransport hands a reader whatever bytes have arrived: a frame can
// span reads, a read can carry several frames and half of another, and a
// large snapshot will always arrive in pieces. The decoder has to be
// indifferent to where the cuts land.

import { describe, expect, it } from "vitest";
import {
  Frame5,
  FrameDecoder,
  Role,
  Wire5Error,
  decodeServerFrame,
  encodeEventRecord,
  encodeReject,
  encodeSnapshot,
  encodeTick,
  encodeWelcome,
  varintLen,
} from "@guardian/chunkies-testkit";

function concat(parts: Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let at = 0;
  for (const p of parts) {
    out.set(p, at);
    at += p.length;
  }
  return out;
}

const welcome = encodeWelcome({
  lineage: 0,
  generation: 0,
  sub: 0,
  epoch: 3,
  seq: 10n,
  tick: 240n,
  hz: 24,
  role: Role.player,
  content: 0xdeadbeefn,
  chunk: "park-mythra",
});
const events = [1, 2, 3].map((n) =>
  encodeTick(BigInt(240 + n), BigInt(10 + n), [
    encodeEventRecord(BigInt(n), 4, 0x9601n, new Uint8Array(n).fill(n)),
  ]),
);
// Long enough to force a two-byte length prefix and to span several reads.
const snapshot = encodeSnapshot({
  lineage: 0,
  seq: 20n,
  tick: 300n,
  epoch: 3,
  wh: 1n,
  content: 0xdeadbeefn,
  z: new Uint8Array(5000).map((_, i) => i & 0xff),
});
const stream = concat([welcome, ...events, snapshot, encodeReject({ intent: 9n, reason: 2 })]);
const expectedKinds = [
  Frame5.welcome,
  Frame5.tick,
  Frame5.tick,
  Frame5.tick,
  Frame5.snapshot,
  Frame5.reject,
];

describe("FrameDecoder", () => {
  it("lifts every frame out of a single read", () => {
    const frames = new FrameDecoder().push(stream);
    expect(frames.map((f) => f.kind)).toEqual(expectedKinds);
    expect(decodeServerFrame(frames[0]!.kind, frames[0]!.body)).toMatchObject({
      kind: "welcome",
      value: { chunk: "park-mythra" },
    });
  });

  it("is indifferent to where a read boundary lands", () => {
    // Every single cut position, including inside a varint prefix, inside
    // a kind byte, and inside a 5KB payload.
    for (let cut = 0; cut <= stream.length; cut++) {
      const d = new FrameDecoder();
      const got = [...d.push(stream.subarray(0, cut)), ...d.push(stream.subarray(cut))];
      expect(
        got.map((f) => f.kind),
        `cut at ${cut}`,
      ).toEqual(expectedKinds);
      expect(d.buffered, `cut at ${cut}`).toBe(0);
    }
  });

  it("resumes across a read that splits the length prefix itself", () => {
    // The prefix is the one big-endian quantity on the wire and it is
    // read before anything knows how long the frame is, so a cut inside
    // it is the case that cannot be handled by buffering "the rest".
    // The snapshot's length needs two bytes, so cutting at 1 lands
    // between them.
    expect(varintLen(snapshot.length - 2)).toBe(2);
    const d = new FrameDecoder();
    expect(d.push(snapshot.subarray(0, 1))).toHaveLength(0);
    expect(d.buffered).toBe(1);
    const frames = d.push(snapshot.subarray(1));
    expect(frames).toHaveLength(1);
    expect(frames[0]!.kind).toBe(Frame5.snapshot);
    expect(decodeServerFrame(frames[0]!.kind, frames[0]!.body)).toMatchObject({
      kind: "snapshot",
      value: { seq: 20n, tick: 300n },
    });
  });

  it("resumes across a read that splits the body", () => {
    const d = new FrameDecoder();
    const mid = Math.floor(snapshot.length / 2);
    expect(d.push(snapshot.subarray(0, mid))).toHaveLength(0);
    expect(d.push(snapshot.subarray(mid))).toHaveLength(1);
  });

  it("resumes across a read that splits the kind byte from its length", () => {
    const d = new FrameDecoder();
    // Exactly the prefix, nothing more: the kind byte arrives next read.
    expect(d.push(welcome.subarray(0, 1))).toHaveLength(0);
    expect(d.push(welcome.subarray(1))).toHaveLength(1);
  });

  it("reassembles a stream delivered one byte at a time", () => {
    const d = new FrameDecoder();
    const got = [];
    for (const byte of stream) got.push(...d.push(Uint8Array.of(byte)));
    expect(got.map((f) => f.kind)).toEqual(expectedKinds);
    expect(got[4]!.body.length).toBe(5044);
  });

  it("holds a partial frame and completes it on the next read", () => {
    const d = new FrameDecoder();
    expect(d.push(welcome.subarray(0, 4))).toHaveLength(0);
    expect(d.buffered).toBe(4);
    expect(d.push(welcome.subarray(4))).toHaveLength(1);
    expect(d.buffered).toBe(0);
  });

  it("does not retain the caller's buffer", () => {
    // Transports reuse read buffers; a decoder that aliased one would
    // decode whatever the next read overwrote it with.
    const d = new FrameDecoder();
    const scratch = welcome.slice();
    d.push(scratch.subarray(0, 4));
    scratch.fill(0);
    const frames = d.push(welcome.subarray(4));
    expect(frames).toHaveLength(1);
    expect(frames[0]!.kind).toBe(Frame5.welcome);
  });

  it("hands back frame bodies that outlive later reads", () => {
    const d = new FrameDecoder();
    const first = d.push(concat([welcome, events[0]!]));
    const bodyBefore = [...first[0]!.body];
    d.push(snapshot);
    expect([...first[0]!.body]).toEqual(bodyBefore);
  });

  it("rejects a length prefix that would buffer unboundedly", () => {
    const d = new FrameDecoder(64);
    expect(() => d.push(Uint8Array.of(0x80, 0x00, 0x10, 0x00))).toThrow(/exceeds 64/);
  });

  it("holds only the bytes that arrived, not the length declared", () => {
    // Nothing is pre-allocated to the declared size. A frame that says it
    // is a legal 1,000,000 bytes and then delivers four costs four, so a
    // peer cannot make the reader reserve memory it never sends.
    const d = new FrameDecoder();
    d.push(Uint8Array.of(0x80, 0x0f, 0x42, 0x40, 1, 2, 3, 4));
    expect(d.buffered).toBe(8);
  });

  it("clears the largest snapshot the park can ever produce", () => {
    // The park module's IO_CAP is 64 KiB, so an uncompressed snapshot
    // cannot exceed it; a snapshot frame is 40 bytes plus the deflate of
    // a state that size. The default read cap must stay well clear of
    // that even in the incompressible worst case, where deflate's stored
    // blocks add a few bytes per 64 KiB rather than removing any.
    const ioCap = 64 * 1024;
    const worstCase = 40 + ioCap + Math.ceil(ioCap / 65535) * 5 + 16;
    const d = new FrameDecoder();
    const frame = encodeSnapshot({
      lineage: 0,
      seq: 1n,
      tick: 1n,
      epoch: 1,
      wh: 1n,
      content: 1n,
      z: new Uint8Array(ioCap),
    });
    expect(frame.length).toBeLessThan(worstCase);
    expect(d.push(frame)).toHaveLength(1);
  });

  it("rejects a zero-length frame", () => {
    expect(() => new FrameDecoder().push(Uint8Array.of(0x00))).toThrow(Wire5Error);
  });

  it("drops buffered bytes on reset, as a redial must", () => {
    const d = new FrameDecoder();
    d.push(welcome.subarray(0, 4));
    d.reset();
    expect(d.buffered).toBe(0);
    expect(d.push(welcome)).toHaveLength(1);
  });
});

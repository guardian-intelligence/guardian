// The v5 codec held to the shared spec: every vector in
// src/chunkies/codec/spec/vectors.txt (read here through the
// Bazel-lockstepped copy in the testkit's goldens) must be reproduced
// byte-identically from the fixture message and must decode cleanly;
// every !vector must fail decode. The Go and Rust suites run the same
// file — the vectors are the spec, the implementations are not.

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { wire5 } from "@guardian/chunkies-testkit";

const goldens = (name: string): string =>
  readFileSync(new URL(`../../chunkies-testkit/goldens/${name}`, import.meta.url), "utf8");

const unhex = (s: string): Uint8Array =>
  Uint8Array.from(s.match(/.{2}/g) ?? [], (b) => Number.parseInt(b, 16));

const hex = (b: Uint8Array): string => [...b].map((n) => n.toString(16).padStart(2, "0")).join("");

const good = new Map<string, Uint8Array>();
const bad = new Map<string, Uint8Array>();
for (const line of goldens("vectors.txt").split("\n")) {
  const trimmed = line.trim();
  if (trimmed === "" || trimmed.startsWith("#")) continue;
  const [name, hexStr] = trimmed.split(" = ");
  (name.startsWith("!") ? bad : good).set(name.replace(/^!/, ""), unhex(hexStr));
}

// The shared fixture values behind every vector.
const fx = {
  lineage: 7,
  generation: 3,
  sub: 0x0d0c0b0a,
  epoch: 2,
  seq: 9n,
  tick: 1024n,
  hz: 24,
  wh: 0xfeedfacecafebeefn,
  content: 0x0123456789abcdefn,
  ct: 1700000000000n,
  intent: 0x1122334455667788n,
  actor: 0xa1a2a3a4a5a6a7a8n,
  kind: 0x0104,
  sysKind: 0x0009,
};

const rec1 = () =>
  wire5.encodeEventRecord(fx.intent, fx.kind, fx.actor, Uint8Array.of(0xde, 0xad, 0xbe, 0xef));
const rec2 = () =>
  wire5.encodeEventRecord(
    wire5.SYSTEM_INTENT,
    fx.sysKind,
    0n,
    Uint8Array.of(0x0a, 0x0b, 0x0c, 0x0d),
  );
const fxZ = Uint8Array.of(0xc0, 0xff, 0xee, 0x00);

// Record vectors (segment/tickrec/watermark/checkpoint) are
// authority-side and Go-only; production TS never touches a WAL.
const goOnly = new Set(["segment", "tickrec", "watermark", "checkpoint"]);

const encoders: Record<string, () => Uint8Array> = {
  hello: () =>
    wire5.encodeHello({
      proto: wire5.PROTO5,
      sinceLineage: fx.lineage,
      sinceSeq: fx.seq,
      sinceTick: fx.tick,
      ticket: new TextEncoder().encode("T-9f"),
    }),
  intent: () =>
    wire5.encodeIntent(fx.intent, fx.kind, fx.actor, Uint8Array.of(0xde, 0xad, 0xbe, 0xef)),
  resync: () => wire5.encodeResync({ lineage: fx.lineage, haveSeq: fx.seq }),
  "resync-neg": () => wire5.encodeResync({ lineage: fx.lineage, haveSeq: -1n }),
  welcome: () =>
    wire5.encodeWelcome({
      lineage: fx.lineage,
      generation: fx.generation,
      sub: fx.sub,
      epoch: fx.epoch,
      seq: fx.seq,
      tick: fx.tick,
      hz: fx.hz,
      role: 1,
      content: fx.content,
      chunk: "park-a",
    }),
  tick: () => wire5.encodeTick(fx.tick, fx.seq, [rec1(), rec2()]),
  reject: () => wire5.encodeReject({ intent: fx.intent, reason: 101 }),
  snapshot: () =>
    wire5.encodeSnapshot({
      lineage: fx.lineage,
      seq: fx.seq,
      tick: fx.tick,
      epoch: fx.epoch,
      wh: fx.wh,
      content: fx.content,
      z: fxZ,
    }),
  check: () => wire5.encodeCheck({ sub: fx.sub, tick: fx.tick, wh: fx.wh, ctMs: fx.ct }),
  verdict: () =>
    wire5.encodeVerdict({
      sub: fx.sub,
      lineage: fx.lineage,
      tick: fx.tick,
      now: fx.ct + 123n,
      ctMs: fx.ct,
      flags: wire5.VERDICT_KNOWN | wire5.VERDICT_OK,
      cw: Uint8Array.of(0x9a, 0xbc, 0xde, 0xf0),
      pw: Uint8Array.of(0x12, 0x34, 0x56, 0x78),
    }),
};

const framePayload = (raw: Uint8Array, wantKind: number): Uint8Array => {
  const { kind, payload } = wire5.splitFrame(raw);
  expect(kind).toBe(wantKind);
  return payload;
};

const decoders: Record<string, (raw: Uint8Array) => unknown> = {
  hello: (raw) => wire5.decodeHello(framePayload(raw, wire5.Frame5.hello)),
  intent: (raw) => wire5.decodeIntent(framePayload(raw, wire5.Frame5.intent)),
  resync: (raw) => wire5.decodeResync(framePayload(raw, wire5.Frame5.resync)),
  "resync-neg": (raw) => wire5.decodeResync(framePayload(raw, wire5.Frame5.resync)),
  welcome: (raw) => wire5.decodeWelcome(framePayload(raw, wire5.Frame5.welcome)),
  tick: (raw) => wire5.decodeTick(framePayload(raw, wire5.Frame5.tick)),
  reject: (raw) => wire5.decodeReject(framePayload(raw, wire5.Frame5.reject)),
  snapshot: (raw) => wire5.decodeSnapshot(framePayload(raw, wire5.Frame5.snapshot)),
  check: (raw) => wire5.decodeCheck(raw),
  verdict: (raw) => wire5.decodeVerdict(raw),
};

describe("wire5 conformance", () => {
  it("covers every vector", () => {
    for (const name of good.keys()) {
      if (!goOnly.has(name)) expect(encoders, name).toHaveProperty(name);
    }
  });

  for (const [name, want] of good) {
    if (goOnly.has(name)) continue;
    it(`encodes ${name} byte-identically`, () => {
      expect(hex(encoders[name]())).toBe(hex(want));
    });
    it(`decodes ${name}`, () => {
      expect(() => decoders[name](want)).not.toThrow();
    });
  }

  for (const [name, raw] of bad) {
    const base = name.split("-")[0];
    if (goOnly.has(base)) continue;
    it(`refuses !${name}`, () => {
      expect(() => decoders[base](raw)).toThrow();
    });
  }

  it("holds the caps to spec/caps.txt", () => {
    const mine: Record<string, number> = {
      PROTO: wire5.PROTO5,
      MAX_FRAME: wire5.MAX_FRAME,
      DG_MAX: wire5.DG_MAX,
    };
    let seen = 0;
    for (const line of goldens("caps.txt").split("\n")) {
      const trimmed = line.trim();
      if (trimmed === "" || trimmed.startsWith("#")) continue;
      const [name, val] = trimmed.split("=");
      if (name === "WAL_MAX_RECORD" || name === "WAL_MAX_CHUNKS") continue; // Go-only, with the records
      expect(mine, name).toHaveProperty(name);
      expect(mine[name], name).toBe(Number(val));
      seen += 1;
    }
    expect(seen).toBe(Object.keys(mine).length);
  });

  it("pins the intent record as a verbatim slice of the tick", () => {
    const intent = good.get("intent");
    const tick = good.get("tick");
    if (intent === undefined || tick === undefined) throw new Error("spec vectors missing");
    const record = hex(wire5.splitFrame(intent).payload);
    expect(hex(tick)).toContain(record);
  });

  it("decodes the tick into the fixture records", () => {
    const tick = good.get("tick");
    if (tick === undefined) throw new Error("spec vector missing");
    const { records } = wire5.decodeTick(wire5.splitFrame(tick).payload);
    expect(records).toHaveLength(2);
    expect(records[0].intent).toBe(fx.intent);
    expect(records[0].actor).toBe(fx.actor);
    expect(hex(records[0].payload)).toBe("deadbeef");
    expect(records[1].intent).toBe(wire5.SYSTEM_INTENT);
    expect(records[1].kind).toBe(fx.sysKind);
  });
});

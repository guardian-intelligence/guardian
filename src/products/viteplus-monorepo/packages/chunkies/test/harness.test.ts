// The deterministic fakes, tested on their own terms: if the harness is
// not reproducible, nothing built on it means anything. The suite that
// drives a real `Core` against the real modules lives in dst.test.ts.

import { describe, expect, it } from "vitest";
import {
  chop,
  encodeVerdict,
  encodeWelcome,
  Harness,
  Role,
  ScriptedTransport,
  seededRandom32,
  VirtualClock,
} from "@guardian/chunkies-testkit";
import { decodeWelcomeEmit } from "../src/status.ts";

const welcome = encodeWelcome({
  epoch: 1,
  seq: 0n,
  tick: 0n,
  hz: 24,
  role: Role.player,
  terrain: 0xdeadbeefn,
  park: "park-mythra",
});

describe("virtual clock", () => {
  it("fires timers in due order, not registration order", () => {
    const clock = new VirtualClock(1000);
    const fired: string[] = [];
    clock.schedule(() => fired.push("late"), 300);
    clock.schedule(() => fired.push("early"), 100);
    clock.advance(500);
    expect(fired).toEqual(["early", "late"]);
    expect(clock.now()).toBe(1500);
  });

  it("plays out a timer that schedules another, if it also comes due", () => {
    const clock = new VirtualClock(0);
    const fired: number[] = [];
    const chain = (n: number) => {
      fired.push(n);
      if (n < 4) clock.schedule(() => chain(n + 1), 100);
    };
    clock.schedule(() => chain(1), 100);
    clock.advance(1000);
    expect(fired).toEqual([1, 2, 3, 4]);
  });

  it("leaves a timer alone until its moment arrives", () => {
    const clock = new VirtualClock(0);
    let fired = false;
    clock.schedule(() => {
      fired = true;
    }, 500);
    clock.advance(499);
    expect(fired).toBe(false);
    expect(clock.nextDue).toBe(1);
    clock.advance(1);
    expect(fired).toBe(true);
    expect(clock.pending).toBe(0);
  });

  it("honours a canceller", () => {
    const clock = new VirtualClock(0);
    let fired = false;
    const cancel = clock.schedule(() => {
      fired = true;
    }, 10);
    cancel();
    clock.advance(100);
    expect(fired).toBe(false);
  });
});

describe("welcome telemetry", () => {
  // welcome(a = epoch, b = hz | role << 32). Packed by the session core,
  // unpacked here; a shift error is silent, so pin the layout.
  it("splits hz from the granted role", () => {
    expect(decodeWelcomeEmit(24n)).toEqual({ hz: 24, role: "spectator" });
    expect(decodeWelcomeEmit(24n | (1n << 32n))).toEqual({ hz: 24, role: "player" });
  });

  it("keeps a full-width hz out of the role bits", () => {
    const hz = 0xffff_ffff;
    expect(decodeWelcomeEmit(BigInt(hz))).toEqual({ hz, role: "spectator" });
    expect(decodeWelcomeEmit(BigInt(hz) | (1n << 32n))).toEqual({ hz, role: "player" });
  });
});

describe("seeded randomness", () => {
  it("replays exactly from a seed", () => {
    const a = seededRandom32(42);
    const b = seededRandom32(42);
    const drawn = Array.from({ length: 16 }, () => a());
    expect(drawn).toEqual(Array.from({ length: 16 }, () => b()));
    expect(drawn.every((n) => Number.isInteger(n) && n >= 0 && n <= 0xffff_ffff)).toBe(true);
  });

  it("diverges on a different seed", () => {
    expect(seededRandom32(1)()).not.toBe(seededRandom32(2)());
  });
});

describe("chop", () => {
  it("hands the remainder to a final read", () => {
    const bytes = Uint8Array.from({ length: 10 }, (_, i) => i);
    expect(chop(bytes, [3, 3]).map((p) => [...p])).toEqual([
      [0, 1, 2],
      [3, 4, 5],
      [6, 7, 8, 9],
    ]);
  });

  it("delivers everything in one read when no sizes are given", () => {
    const bytes = new Uint8Array(5);
    expect(chop(bytes)).toEqual([bytes]);
  });

  it("stops once the bytes run out", () => {
    expect(chop(new Uint8Array(2), [5, 5, 5])).toHaveLength(1);
  });
});

describe("scripted transport", () => {
  const sink = () => {
    const stream: Uint8Array[] = [];
    const datagrams: Uint8Array[] = [];
    let closes = 0;
    return {
      stream,
      datagrams,
      get closes() {
        return closes;
      },
      sink: {
        onStreamBytes: (b: Uint8Array) => stream.push(b.slice()),
        onDatagram: (b: Uint8Array) => datagrams.push(b.slice()),
        onClosed: () => {
          closes++;
        },
      },
    };
  };

  it("delivers a frame in the read sizes the script names", async () => {
    const t = new ScriptedTransport();
    const s = sink();
    await t.connect(s.sink);
    t.deliverFrames([welcome], [4, 4]);
    expect(s.stream.map((p) => p.length)).toEqual([4, 4, welcome.length - 8]);
  });

  it("replays a dial script, then repeats its last outcome", async () => {
    const t = new ScriptedTransport();
    t.outcomes = [{ ok: false, error: "handshake timeout" }, { ok: true }];
    const s = sink();
    await expect(t.connect(s.sink)).rejects.toThrow("handshake timeout");
    await expect(t.connect(s.sink)).resolves.toBeTruthy();
    await expect(t.connect(s.sink)).resolves.toBeTruthy();
    expect(t.dials).toBe(3);
  });

  it("records what the core wrote, decoded", async () => {
    const t = new ScriptedTransport();
    const s = sink();
    const dialed = await t.connect(s.sink);
    // Two frames written in one call each, and one split across two — the
    // recorder must not care.
    const resync = new Uint8Array([0x09, 0x03, 0x07, 0, 0, 0, 0, 0, 0, 0]);
    dialed.connection.sendFrame(resync);
    dialed.connection.sendFrame(resync.subarray(0, 3));
    dialed.connection.sendFrame(resync.subarray(3));
    expect(t.sentFrames()).toEqual([
      { kind: "resync", value: { haveSeq: 7n } },
      { kind: "resync", value: { haveSeq: 7n } },
    ]);
  });

  it("reports a drop exactly once and refuses further delivery", async () => {
    const t = new ScriptedTransport();
    const s = sink();
    await t.connect(s.sink);
    t.drop();
    expect(s.closes).toBe(1);
    expect(t.live).toBe(false);
    expect(() => t.deliverDatagram(new Uint8Array(1))).toThrow(/no connection/);
  });

  it("delivers datagrams in whatever order the script gives", async () => {
    const t = new ScriptedTransport();
    const s = sink();
    await t.connect(s.sink);
    const later = encodeVerdict({
      tick: 20n,
      now: 2n,
      ctMs: 1n,
      known: true,
      ok: true,
      cw: new Uint8Array(4),
      pw: new Uint8Array(4),
    });
    const earlier = encodeVerdict({
      tick: 10n,
      now: 1n,
      ctMs: 0n,
      known: true,
      ok: true,
      cw: new Uint8Array(4),
      pw: new Uint8Array(4),
    });
    t.deliverDatagram(later);
    t.deliverDatagram(earlier);
    expect(s.datagrams.map((d) => new DataView(d.buffer).getBigUint64(1, true))).toEqual([
      20n,
      10n,
    ]);
  });
});

describe("harness ports", () => {
  it("records fetches and fails the ones with no fixture", async () => {
    const replica = new ArrayBuffer(8);
    const h = new Harness({ modules: { replica } });
    await expect(h.ports.fetchModule("replica", "abc12345")).resolves.toBe(replica);
    await expect(h.ports.fetchModule("session")).rejects.toThrow("no session module");
    await expect(h.ports.fetchBlob(1, "00000000deadbeef")).rejects.toThrow("no blob");
    expect(h.moduleFetches).toEqual([{ slot: "replica", ref: "abc12345" }, { slot: "session" }]);
    expect(h.blobFetches).toEqual(["00000000deadbeef"]);
  });

  it("reads time only from the virtual clock", () => {
    const h = new Harness({ startMs: 5000 });
    expect(h.hostOptions.now()).toBe(5000);
    h.clock.advance(250);
    expect(h.hostOptions.now()).toBe(5250);
  });

  it("collects the telemetry vocabulary as raw codes", () => {
    const h = new Harness();
    h.hostOptions.telemetry(2, 7n, 24n);
    h.hostOptions.telemetry(14, 1n, 2n);
    expect(h.codes()).toEqual([2, 14]);
    expect(h.emitted[0]).toEqual({ code: 2, a: 7n, b: 24n });
  });

  it("gives two harnesses on one seed the same draws", () => {
    const draw = (seed: number) => {
      const h = new Harness({ seed });
      return [h.ports.random32(), h.ports.random32()];
    };
    expect(draw(7)).toEqual(draw(7));
    expect(draw(7)).not.toEqual(draw(8));
  });
});

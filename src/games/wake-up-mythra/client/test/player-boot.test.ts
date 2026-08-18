// A player joining a park that is already running, over a network with a
// round trip. The replica is deliberately optimistic — it steps ahead of
// what it has been told so the world feels live — but the size of that
// optimism is bounded by the trip time, not free. An event arrives
// stamped where the authority was when it journaled it; the replica
// should be at most half a round trip past that.

import { describe, expect, it } from "vitest";
import { Emit } from "@guardian/chunkies";
import { dogPayload, echoPayload, Ev, Role } from "@guardian/chunkies-testkit";
import { wumRig, type WumRig } from "./rig.ts";

const TICK_MS = 1000 / 24;
/** The ring keeps one entry a second, so a repair reaches back at most this far. */
const RING_CADENCE_TICKS = 24;
/** One terrain cell in the Q16.16 coordinates sim_view reports. */
const ONE_CELL = 65536;

/**
 * Half a round trip, in ticks, plus a tick of slack for the frame the
 * replica happens to be mid-way through. Anything past this is drift the
 * clock introduced rather than latency the client is compensating for.
 */
function lagBudget(rttMs: number): number {
  return Math.ceil(rttMs / 2 / TICK_MS) + 1;
}

/**
 * Runs the park on the wall clock, independent of the replica. A world
 * that only steps when the client does cannot drift away from it, which
 * is the thing under test.
 */
function liveWorld(r: WumRig) {
  const startMs = r.harness.clock.now();
  const startTick = r.authority.tick;
  return () => {
    const due = startTick + BigInt(Math.floor((r.harness.clock.now() - startMs) / TICK_MS));
    while (r.authority.tick < due) r.authority.step();
  };
}

type Sample = { at: bigint; replica: bigint; authority: bigint; ahead: number };

/** Every dog in a `sim_view` projection, as id -> its exact Q16.16 position. */
function dogsIn(view: Uint8Array): Map<bigint, string> {
  const dv = new DataView(view.buffer, view.byteOffset, view.byteLength);
  const out = new Map<bigint, string>();
  const n = dv.getUint32(0, true);
  for (let i = 0; i < n; i++) {
    const at = 4 + i * 20;
    out.set(
      dv.getBigUint64(at, true),
      `${dv.getInt32(at + 8, true)},${dv.getInt32(at + 12, true)}`,
    );
  }
  return out;
}

describe("a player attaching to a running park", () => {
  it("never runs further ahead of arriving events than the trip time explains", async () => {
    const RTT = 200;
    const r = await wumRig({ role: "player", checkMs: 200, myDog: 0x7001n });
    const step = liveWorld(r);
    const samples: Sample[] = [];

    // Let the park get somewhere before we attach: a fresh park at tick 0
    // hides every ordering problem that only exists mid-session.
    for (let t = 0; t < 2000; t += 20) {
      r.harness.clock.advance(20);
      step();
      await r.harness.settle();
    }

    // The welcome describes the world as it was when the authority sent
    // it, and reaches us half a trip later.
    const welcome = r.authority.welcome(Role.player);
    const snapshot = r.authority.snapshot();
    for (let t = 0; t < RTT / 2; t += 20) {
      r.harness.clock.advance(20);
      step();
      r.pump();
      await r.harness.settle();
    }
    r.deliver([welcome, snapshot]);

    // From here the session is live: pump every frame, keep the park
    // running, and answer checks over a real round trip.
    const pumpFor = async (ms: number) => {
      for (let t = 0; t < ms; t += 16) {
        r.harness.clock.advance(16);
        step();
        r.pump();
        r.answerChecksOverRtt(RTT);
        await r.harness.settle();
      }
    };
    await pumpFor(1500);

    // The journal answers our join: the authority stamped it when the
    // intent arrived, and it reaches us half a trip after that.
    const joinEvent = r.authority.apply(Ev.join, dogPayload(0x7001n));
    const stampedAt = r.authority.tick;
    await pumpFor(RTT / 2);
    samples.push({
      at: r.state.tick,
      replica: r.state.tick,
      authority: r.authority.tick,
      ahead: Number(r.state.tick - stampedAt),
    });
    r.deliver([joinEvent]);
    await pumpFor(1000);

    // And a stream of ordinary traffic, each event stamped where the park
    // was and arriving a half trip later.
    for (let i = 0; i < 6; i++) {
      const frame = r.authority.apply(Ev.join, dogPayload(BigInt(0x7100 + i)));
      const stamp = r.authority.tick;
      await pumpFor(RTT / 2);
      samples.push({
        at: r.state.tick,
        replica: r.state.tick,
        authority: r.authority.tick,
        ahead: Number(r.state.tick - stamp),
      });
      r.deliver([frame]);
      await pumpFor(400);
    }

    const budget = lagBudget(RTT);
    const worst = Math.max(...samples.map((s) => s.ahead));
    expect(worst).toBeLessThanOrEqual(budget);
  });

  it("repairs a late event within the frame, without showing the rewind", async () => {
    // Rewinding and replaying are how a late event is absorbed, and both
    // belong to the frame that absorbs it. A frame that ends earlier than
    // the one before it is a world that visibly jumps backwards and then
    // races to catch up, which is the repair leaking onto the screen.
    const RTT = 200;
    const r = await wumRig({ role: "player", checkMs: 200, myDog: 0x7003n });
    const step = liveWorld(r);
    for (let t = 0; t < 2000; t += 20) {
      r.harness.clock.advance(20);
      step();
      await r.harness.settle();
    }
    // Bystanders: dogs already in the park when we attach, with nothing to
    // do with the event that arrives late.
    for (let i = 0; i < 8; i++) r.authority.apply(Ev.join, dogPayload(BigInt(0x7200 + i)));
    r.deliver([r.authority.welcome(Role.player), r.authority.snapshot()]);

    const frames: { tick: bigint; dogs: Map<bigint, string> }[] = [];
    const observe = () => {
      const view = r.frame();
      if (view) frames.push({ tick: view.tick, dogs: dogsIn(view.viewBytes) });
    };

    const pumpFor = async (ms: number) => {
      for (let t = 0; t < ms; t += 16) {
        r.harness.clock.advance(16);
        step();
        r.pump();
        r.answerChecksOverRtt(RTT);
        observe();
        await r.harness.settle();
      }
    };
    await pumpFor(1500);

    // An event the authority stamped where it was when it journaled it,
    // reaching us a half trip later — by then we have stepped past it.
    const frame = r.authority.apply(Ev.join, dogPayload(0x7300n));
    const stamp = r.authority.tick;
    await pumpFor(RTT / 2);
    expect(Number(r.state.tick - stamp)).toBeGreaterThan(0);

    const settled = frames.length;
    const before = frames.at(-1)!;
    r.deliver([frame]);
    await pumpFor(400);
    expect(r.state.rollbacks).toBeGreaterThan(0);

    // No frame after the repair may show the world earlier than the frame
    // that preceded it.
    for (let i = settled; i < frames.length; i++) {
      expect(frames[i]!.tick, `frame ${i} went backwards`).toBeGreaterThanOrEqual(
        frames[i - 1]!.tick,
      );
    }

    // And a dog seen at the same tick must be in the same place: the
    // repair reproduces the world it rewound through, it does not re-roll
    // it.
    const after = frames[settled]!;
    expect(after.tick).toBeGreaterThanOrEqual(before.tick);
    if (after.tick === before.tick) {
      let compared = 0;
      for (const [id, was] of before.dogs) {
        if (id === 0x7003n) continue;
        const now = after.dogs.get(id);
        if (now === undefined) continue;
        compared++;
        expect(now, `bystander ${id.toString(16)} moved without time passing`).toBe(was);
      }
      expect(compared).toBeGreaterThan(0);
    }
  });

  it("shows our dog once the journal places it, and not before", async () => {
    // Where a joining dog lands is decided at the tick the join is
    // applied, which is the park's to decide and no one else's. The world
    // simply does not carry a dog the journal has not placed.
    const RTT = 200;
    const r = await wumRig({ role: "player", checkMs: 200, myDog: 0x7005n, rttMs: RTT });
    const step = liveWorld(r);
    for (let t = 0; t < 2000; t += 20) {
      r.harness.clock.advance(20);
      step();
      await r.harness.settle();
    }
    for (let i = 0; i < 4; i++) r.authority.apply(Ev.join, dogPayload(BigInt(0x7500 + i)));
    r.deliver([r.authority.welcome(Role.player), r.authority.snapshot()]);

    const seen = new Set<string>();
    const pumpFor = async (ms: number) => {
      for (let t = 0; t < ms; t += 16) {
        r.harness.clock.advance(16);
        step();
        r.pump();
        r.answerChecks();
        const view = r.frame();
        const mine = view && dogsIn(view.viewBytes).get(0x7005n);
        if (mine) seen.add(mine);
        await r.harness.settle();
      }
    };
    await pumpFor(600);
    // The join is on the wire and unanswered, so the park we present is
    // the park the journal describes: four dogs, none of them ours.
    expect(r.state.present).toBe(false);
    expect(r.state.dogCount).toBe(4);
    expect(seen.size).toBe(0);

    // Other dogs keep arriving and the world keeps changing; ours still is
    // not in it, because nothing has placed it yet.
    for (let i = 0; i < 4; i++) {
      r.deliver([r.authority.apply(Ev.join, dogPayload(BigInt(0x7600 + i)))]);
      await pumpFor(300);
    }
    expect(seen.size).toBe(0);
    expect(r.state.present).toBe(false);

    // The journal places it. From then on it is an ordinary dog: it may
    // walk, but it may not jump — a placed dog moves at most a cell
    // between frames, where a re-rolled spawn moves it across the park.
    r.deliver([r.authority.apply(Ev.join, dogPayload(0x7005n))]);
    const track: [number, number][] = [];
    for (let t = 0; t < 800; t += 16) {
      r.harness.clock.advance(16);
      step();
      r.pump();
      r.answerChecks();
      const view = r.frame();
      const mine = view && dogsIn(view.viewBytes).get(0x7005n);
      if (mine) {
        const [x, y] = mine.split(",").map(Number);
        track.push([x!, y!]);
      }
      await r.harness.settle();
    }
    expect(r.state.present).toBe(true);
    expect(track.length).toBeGreaterThan(0);
    for (let i = 1; i < track.length; i++) {
      const dx = Math.abs(track[i]![0] - track[i - 1]![0]);
      const dy = Math.abs(track[i]![1] - track[i - 1]![1]);
      expect(Math.max(dx, dy), `frame ${i}: dog jumped`).toBeLessThan(ONE_CELL);
    }
  });

  it("plays a clean leg cleanly", async () => {
    // The baseline every other case is a deviation from. A player on a
    // fast link runs the same module on the same events, so nothing here
    // should need a snapshot to repair it. Rollbacks are not zero and
    // cannot be: an own action is journaled at the authority's tick,
    // which a leading replica has already passed, so each one costs a
    // rewind the size of the lead. That is the price of freshness, and
    // in-frame replay is what makes it invisible rather than free.
    const r = await wumRig({ role: "player", checkMs: 500, myDog: 0x8001n, rttMs: 0, seed: 11 });
    const step = liveWorld(r);
    for (let t = 0; t < 1000; t += 20) {
      r.harness.clock.advance(20);
      step();
      await r.harness.settle();
    }
    for (let i = 0; i < 6; i++) r.authority.apply(Ev.join, dogPayload(BigInt(0x8100 + i)));
    r.deliver([r.authority.welcome(Role.player), r.authority.snapshot()]);

    // The authority journals an intent when it arrives, at the tick it
    // holds then, and sends the event back — an intent's real round trip.
    let forwarded = 0;
    const journalIntents = () => {
      const frames = r.harness.transport.sentFrames();
      for (; forwarded < frames.length; forwarded++) {
        const f = frames[forwarded]!;
        if (f.kind !== "intent") continue;
        try {
          r.deliver([r.authority.apply(f.value.kind, echoPayload(f.value), f.value.intent)]);
        } catch {
          // Refused; rejects are another case's business.
        }
      }
    };

    const presented: bigint[] = [];
    let maxLead = 0;
    let moves = 0;
    let nextMove = 1500;
    const mark = r.harness.emitted.length;
    for (let t = 0; t < 11_000; t += 16) {
      r.harness.clock.advance(16);
      step();
      r.pump();
      r.answerChecks();
      if (r.state.tick > 0n) journalIntents();
      if (t >= nextMove) {
        r.moveTo(200 + ((moves * 37) % 4000));
        moves++;
        nextMove = t + 900;
      }
      const view = r.frame();
      if (view) presented.push(view.tick);
      if (r.state.tick > 0n) {
        maxLead = Math.max(maxLead, Number(r.state.tick - r.authority.tick));
      }
      await r.harness.settle();
    }

    // rollback(returned_to, late << 32 | rewound)
    const rolls = r.harness.emitted
      .slice(mark)
      .filter((e) => e.code === Emit.rollback)
      .map((e) => ({ late: Number(e.b >> 32n), rewound: Number(e.b & 0xffffffffn) }));

    expect(moves).toBeGreaterThan(8);
    expect(r.state.present).toBe(true);

    // Nothing on a clean leg is beyond local repair.
    expect(Number(r.state.resyncs)).toBe(0);

    // One repair per own action at most: our own events are journalled at
    // the authority's tick and so arrive behind a replica that has already
    // stepped past it, and nothing else on a clean leg is late at all.
    expect(rolls.length).toBeLessThanOrEqual(moves + 1);

    for (const roll of rolls) {
      // A repair reaches back to the event, and from there to the newest
      // retained state older than it. Entries land one a second, so how
      // much further than the event it has to reach is wherever that
      // event fell between two of them — never a whole cadence more.
      expect(roll.late).toBeLessThanOrEqual(maxLead + 1);
      expect(roll.rewound).toBeGreaterThanOrEqual(roll.late);
      expect(roll.rewound).toBeLessThan(roll.late + RING_CADENCE_TICKS);
    }

    // And none of it is visible: the world never steps backwards.
    for (let i = 1; i < presented.length; i++) {
      expect(presented[i], `frame ${i} went backwards`).toBeGreaterThanOrEqual(presented[i - 1]!);
    }
  });

  it("tracks the authority's tick rather than drifting away from it", async () => {
    // The clock is disciplined by verdicts, which carry the authority's
    // tick. However far the replica runs ahead to feel live, the gap must
    // stay near the trip time instead of growing.
    const RTT = 300;
    const r = await wumRig({ role: "player", checkMs: 200, myDog: 0x7002n });
    const step = liveWorld(r);
    for (let t = 0; t < 1500; t += 20) {
      r.harness.clock.advance(20);
      step();
      await r.harness.settle();
    }
    r.deliver([r.authority.welcome(Role.player), r.authority.snapshot()]);

    const gaps: number[] = [];
    for (let t = 0; t < 8000; t += 16) {
      r.harness.clock.advance(16);
      step();
      r.pump();
      r.answerChecksOverRtt(RTT);
      await r.harness.settle();
      if (t % 400 === 0 && r.state.tick > 0n) {
        gaps.push(Number(r.state.tick - r.authority.tick));
      }
    }
    const budget = lagBudget(RTT);
    // Late in the session the clock has had many samples; the gap should
    // be settled, not still growing.
    const settled = gaps.slice(Math.floor(gaps.length / 2));
    for (const g of settled) expect(Math.abs(g)).toBeLessThanOrEqual(budget + 2);
  });
});

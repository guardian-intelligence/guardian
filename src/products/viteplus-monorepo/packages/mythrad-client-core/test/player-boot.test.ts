// A player joining a park that is already running, over a network with a
// round trip. The replica is deliberately optimistic — it steps ahead of
// what it has been told so the world feels live — but the size of that
// optimism is bounded by the trip time, not free. An event arrives
// stamped where the authority was when it journaled it; the replica
// should be at most half a round trip past that.

import { describe, expect, it } from "vitest";
import { Emit } from "../src/ports.ts";
import { Role } from "../src/wire.ts";
import { Ev, dogPayload, rig, type Rig } from "./wasm.ts";

const TICK_MS = 1000 / 24;

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
function liveWorld(r: Rig) {
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
    const r = await rig({ role: "player", checkMs: 200, myDog: 0x7001n });
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
      r.core.pump();
      await r.harness.settle();
    }
    r.deliver([welcome, snapshot]);

    // From here the session is live: pump every frame, keep the park
    // running, and answer checks over a real round trip.
    const pumpFor = async (ms: number) => {
      for (let t = 0; t < ms; t += 16) {
        r.harness.clock.advance(16);
        step();
        r.core.pump();
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
      at: r.core.state.tick,
      replica: r.core.state.tick,
      authority: r.authority.tick,
      ahead: Number(r.core.state.tick - stampedAt),
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
        at: r.core.state.tick,
        replica: r.core.state.tick,
        authority: r.authority.tick,
        ahead: Number(r.core.state.tick - stamp),
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
    const r = await rig({ role: "player", checkMs: 200, myDog: 0x7003n });
    const step = liveWorld(r);
    for (let t = 0; t < 2000; t += 20) {
      r.harness.clock.advance(20);
      step();
      await r.harness.settle();
    }
    r.deliver([r.authority.welcome(Role.player), r.authority.snapshot()]);

    const frames: { tick: bigint; dogs: Map<bigint, string> }[] = [];
    const observe = () => {
      const view = r.core.view();
      if (view) frames.push({ tick: view.tick, dogs: dogsIn(view.viewBytes) });
    };

    const pumpFor = async (ms: number) => {
      for (let t = 0; t < ms; t += 16) {
        r.harness.clock.advance(16);
        step();
        r.core.pump();
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
    expect(Number(r.core.state.tick - stamp)).toBeGreaterThan(0);

    const settled = frames.length;
    const before = frames.at(-1)!;
    r.deliver([frame]);
    await pumpFor(400);
    expect(r.core.state.rollbacks).toBeGreaterThan(0);

    // No frame after the repair may show the world earlier than the frame
    // that preceded it.
    for (let i = settled; i < frames.length; i++) {
      expect(frames[i]!.tick, `frame ${i} went backwards`).toBeGreaterThanOrEqual(
        frames[i - 1]!.tick,
      );
    }

    // And the dogs that had nothing to do with the event must not be seen
    // standing where they stood several ticks ago.
    const after = frames[settled]!;
    expect(after.tick).toBeGreaterThanOrEqual(before.tick);
    for (const [id, was] of before.dogs) {
      const now = after.dogs.get(id);
      if (now === undefined) continue;
      if (after.tick === before.tick) expect(now).toBe(was);
    }
  });

  it("rewinds no further than the event was late", async () => {
    // Repairing a late event costs a replay over every tick between the
    // state it rewinds to and the present, and that replay is real work on
    // the frame it lands: for a full park, thousands of dog steps. So the
    // cost has to follow how late the event was, not how long ago the last
    // retained state happens to be. A rewind that cannot be paid for
    // inside the frame's budget is a resync, not a longer replay.
    const RTT = 200;
    const r = await rig({ role: "player", checkMs: 200, myDog: 0x7004n, rttMs: RTT });
    const step = liveWorld(r);
    for (let t = 0; t < 2000; t += 20) {
      r.harness.clock.advance(20);
      step();
      await r.harness.settle();
    }
    r.deliver([r.authority.welcome(Role.player), r.authority.snapshot()]);

    const pumpFor = async (ms: number) => {
      for (let t = 0; t < ms; t += 16) {
        r.harness.clock.advance(16);
        step();
        r.core.pump();
        r.answerChecks();
        await r.harness.settle();
      }
    };
    await pumpFor(1500);

    const frame = r.authority.apply(Ev.join, dogPayload(0x7400n));
    const stamp = r.authority.tick;
    await pumpFor(RTT / 2);
    const lateBy = Number(r.core.state.tick - stamp);
    expect(lateBy).toBeGreaterThan(0);

    const mark = r.harness.emitted.length;
    r.deliver([frame]);
    await pumpFor(400);

    // rollback(late_ticks, rewound_ticks): how late the event was, and how
    // far back the repair had to reach in order to place it.
    const rolls = r.harness.emitted
      .slice(mark)
      .filter((e) => e.code === Emit.rollback)
      .map((e) => ({ late: Number(e.a), rewound: Number(e.b) }));
    expect(rolls.length).toBeGreaterThan(0);
    for (const roll of rolls) {
      expect(roll.late).toBeLessThanOrEqual(lateBy + 1);
      expect(roll.rewound).toBeLessThanOrEqual(roll.late + 1);
    }
  });

  it("tracks the authority's tick rather than drifting away from it", async () => {
    // The clock is disciplined by verdicts, which carry the authority's
    // tick. However far the replica runs ahead to feel live, the gap must
    // stay near the trip time instead of growing.
    const RTT = 300;
    const r = await rig({ role: "player", checkMs: 200, myDog: 0x7002n });
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
      r.core.pump();
      r.answerChecksOverRtt(RTT);
      await r.harness.settle();
      if (t % 400 === 0 && r.core.state.tick > 0n) {
        gaps.push(Number(r.core.state.tick - r.authority.tick));
      }
    }
    const budget = lagBudget(RTT);
    // Late in the session the clock has had many samples; the gap should
    // be settled, not still growing.
    const settled = gaps.slice(Math.floor(gaps.length / 2));
    for (const g of settled) expect(Math.abs(g)).toBeLessThanOrEqual(budget + 2);
  });
});

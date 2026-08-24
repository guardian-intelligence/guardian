// One journey through a live tick-rate change, as a player's browser
// experiences it. Mid-session the authority journals an epoch advance
// carrying 48Hz, and later one back to 24Hz. Across each boundary the
// client must adopt the rate from the event itself, rebuild the world
// exactly once from a snapshot taken under the new era, keep its
// transport and its dog, keep serving intents, and then run at the new
// rate — tracking the authority's tick rather than drifting from it.
// This is the contract `aspect mythra dev latency` proves against a real
// stack; here it runs against the committed modules in lockstep.
import { describe, expect, it } from "vitest";
import { Emit } from "@guardian/chunkies";
import { echoPayload, epochAdvancePayload, Ev, Role } from "@guardian/chunkies-testkit";
import { wumRig, type WumRig } from "./rig.ts";

const RTT = 120;

/** Half a trip, in ticks at `hz`, plus one: how far a locked replica may sit from the authority. */
function lagBudget(hz: number): number {
  return Math.ceil(((RTT / 2) * hz) / 1000) + 1;
}

/** Steps the authority on the wall clock at whatever rate it currently journals. */
function liveWorld(r: WumRig) {
  let anchorMs = r.harness.clock.now();
  let anchorTick = r.authority.tick;
  let hz = r.authority.hz;
  return () => {
    const now = r.harness.clock.now();
    if (r.authority.hz !== hz) {
      anchorMs = now;
      anchorTick = r.authority.tick;
      hz = r.authority.hz;
    }
    const due = anchorTick + BigInt(Math.floor(((now - anchorMs) * hz) / 1000));
    while (r.authority.tick < due) r.authority.step();
  };
}

describe("a live tick-rate change", () => {
  it("is one epoch the player rides through, then plays at the new rate", async () => {
    const r = await wumRig({ role: "player", checkMs: 200, myDog: 0x7a7en, rttMs: RTT, seed: 5 });
    const step = liveWorld(r);

    // The authority journals an intent when it arrives, at the tick it
    // holds then, and sends the event back — an intent's real round trip.
    let forwarded = 0;
    const journalIntents = () => {
      const frames = r.harness.transport.sentFrames();
      for (; forwarded < frames.length; forwarded++) {
        const f = frames[forwarded]!;
        if (f.kind !== "intent") continue;
        r.deliver([r.authority.apply(f.value.kind, echoPayload(f.value), f.value.intent)]);
      }
    };
    const play = async (ms: number) => {
      const from = { replica: r.state.tick, authority: r.authority.tick };
      for (let t = 0; t < ms; t += 16) {
        r.harness.clock.advance(16);
        step();
        r.pump();
        r.answerChecks();
        r.answerResyncs();
        if (r.state.tick > 0n) journalIntents();
        await r.harness.settle();
      }
      return {
        replica: Number(r.state.tick - from.replica),
        authority: Number(r.authority.tick - from.authority),
      };
    };

    // Attach to a running 24Hz park and lock.
    await play(1000);
    r.deliver([r.authority.welcome(Role.player), r.authority.snapshot()]);
    await play(3000);
    expect(r.state.hz).toBe(24);
    expect(r.state.clockState).toBe("locked");
    expect(r.state.present).toBe(true);
    const baseline = await play(2000);
    expect(baseline.authority).toBeGreaterThanOrEqual(46);
    expect(baseline.authority).toBeLessThanOrEqual(49);
    expect(Math.abs(baseline.replica - baseline.authority)).toBeLessThanOrEqual(4);

    let moves = 0;
    const cross = async (epoch: number, from: number, to: number) => {
      const dials = r.count(Emit.connectedHelloSent);
      const resyncs = r.count(Emit.resyncRequested);
      const restores = r.count(Emit.snapshotRestored);
      const rateChanges = r.count(Emit.rateChanged);
      const boundary = r.authority.tick;

      // Same module, new rate: the era changes, so the world is rebuilt.
      r.deliver([r.authority.apply(Ev.epochAdvance, epochAdvancePayload(epoch, 0n, to))]);

      // The rate comes from the event, at the tick it was journaled —
      // before any snapshot has landed.
      await play(64);
      expect(r.state.hz).toBe(to);
      expect(r.count(Emit.rateChanged)).toBe(rateChanges + 1);
      expect(r.harness.emitted.filter((e) => e.code === Emit.rateChanged).at(-1)).toMatchObject({
        a: boundary,
        b: (BigInt(from) << 32n) | BigInt(to),
      });

      // Exactly one rebuild, on the same connection, with the same module,
      // and our dog still in the park.
      await play(RTT * 4);
      expect(r.count(Emit.resyncRequested)).toBe(resyncs + 1);
      expect(r.count(Emit.snapshotRestored)).toBe(restores + 1);
      expect(r.count(Emit.connectedHelloSent)).toBe(dials);
      expect(r.count(Emit.moduleSwapWanted)).toBe(0);
      expect(r.state.present).toBe(true);

      // The session keeps serving: a move after the boundary is journaled
      // under the new era and answered.
      const answered = r.count(Emit.intentAnswered);
      r.moveTo(200 + ((moves++ * 37) % 4000));
      await play(RTT * 4);
      expect(r.count(Emit.intentAnswered)).toBe(answered + 1);

      // And the world now runs at the new rate, with the replica keeping
      // pace with the authority rather than drifting from it.
      await play(1000);
      const leg = await play(2000);
      expect(leg.authority).toBeGreaterThanOrEqual(2 * to - 2);
      expect(leg.authority).toBeLessThanOrEqual(2 * to + 1);
      expect(Math.abs(leg.replica - leg.authority)).toBeLessThanOrEqual(4);
      expect(r.state.clockState).toBe("locked");
      expect(Math.abs(Number(r.state.tick - r.authority.tick))).toBeLessThanOrEqual(
        lagBudget(to) + 2,
      );
    };

    await cross(2, 24, 48);
    await cross(3, 48, 24);
  });
});

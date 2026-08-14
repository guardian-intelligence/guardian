// Repairs that replay events the park REFUSES on a second application.
//
// The park is not indifferent to being told something twice: a second
// check-in is ERR_CHECKED_IN, a boost that already matches is ERR_NOOP. So
// a replay that re-applies an event the restored floor already contains
// does not quietly do nothing — it is turned down, and a replica that
// treats "refused" as "applied" (or the reverse) carries a world the park
// does not have. Nothing about that is visible in a leg total: the dogs
// are all present, the seq is dense, and the only symptom is a hash the
// authority disagrees with.
//
// The authority here is a real park instance, so it refuses exactly what
// production refuses. That is the whole point of this file.

import { describe, expect, it } from "vitest";
import {
  bringTheDogIn,
  DEFAULT_RTT_MS,
  dogPayload,
  Ev,
  intentId,
  intentsSent,
  rig,
} from "@guardian/chunkies-testkit";

const ONE_WAY_MS = DEFAULT_RTT_MS / 2;

/** The 9-byte boost_set payload: dog id, then on/off. */
function boostPayload(dog: bigint, on: boolean): Uint8Array {
  const p = new Uint8Array(9);
  const dv = new DataView(p.buffer);
  dv.setBigUint64(0, dog, true);
  p[8] = on ? 1 : 0;
  return p;
}

describe("a repair that replays a refusable event", () => {
  it("keeps the replica's world the park's world", async () => {
    const dog = 0x9601n;
    const r = await rig({ role: "player", myDog: dog, checkMs: 150 });
    await r.establish();
    await bringTheDogIn(r, dog);

    // The park answers intents over a round trip, so our own events come
    // back stamped behind the replica and every one buys a repair — which
    // is what puts a replay over the events below.
    let cursor = r.harness.transport.sentFrames().length;
    const answer = () => {
      const written = r.harness.transport.sentFrames();
      for (; cursor < written.length; cursor++) {
        const f = written[cursor]!;
        if (f.kind !== "intent") continue;
        const intent = f.value;
        r.harness.clock.schedule(() => {
          const ev = r.authority.apply(intent.kind, intent.p, intent.id);
          r.harness.clock.schedule(() => {
            r.deliver([ev]);
          }, ONE_WAY_MS);
        }, ONE_WAY_MS);
      }
    };

    const mismatchesBefore = r.state.mismatches;
    const eventsBefore = r.state.events;
    const rollbacksBefore = r.state.rollbacks;

    // Check in once — the park refuses a second one for the rest of the
    // day — and toggle boost repeatedly, which the park refuses whenever
    // the value already matches. Both land while repairs are in flight.
    let checkedIn = false;
    let boosts = 0;
    let nextBoost = 200;
    for (let ms = 0; ms < 4000; ms += 8) {
      r.harness.clock.advance(8);
      r.pump();
      while (r.authority.tick < r.state.tick) r.authority.step();
      r.answerChecks();
      answer();
      if (!checkedIn && ms >= 100) {
        r.checkIn();
        checkedIn = true;
      }
      if (ms >= nextBoost && boosts < 8) {
        r.setBoost(boosts % 2 === 0);
        boosts++;
        nextBoost = ms + 400;
      }
      await r.harness.settle();
    }

    // Coverage before verdict: the park has to have journalled these and
    // the session has to have repaired around them, or this case is a
    // quiet session proving nothing.
    expect(r.state.events - eventsBefore, "events applied").toBeGreaterThan(4);
    expect(r.state.rollbacks - rollbacksBefore, "repairs performed").toBeGreaterThan(0);
    expect(intentsSent(r, Ev.checkIn).length, "a check-in was sent").toBeGreaterThan(0);
    expect(intentId(intentsSent(r, Ev.boostSet)[0]), "a boost was sent").toBeGreaterThan(0n);

    // And the worlds still agree. A replay that mishandled a refusal would
    // show up here and nowhere else.
    expect(r.state.mismatches, "hash mismatches").toBe(mismatchesBefore);
    expect(r.state.resyncs, "resyncs").toBe(0);
  });

  it("survives the journal repeating what the world already has", async () => {
    // The same refusal from the other direction: the authority journals a
    // boost that matches the world, and a late arrival forces the replica
    // to replay across it. The park refuses the repeat both times, and the
    // two worlds must still agree about what happened.
    const dog = 0x9602n;
    const r = await rig({ role: "player", myDog: dog, checkMs: 150 });
    await r.establish();
    await bringTheDogIn(r, dog);
    await r.run(200);

    const mismatchesBefore = r.state.mismatches;
    r.deliver([r.emit(Ev.boostSet, boostPayload(dog, true))]);
    expect(await r.until(() => r.state.seq === r.authority.seq, 2000)).toBe(true);

    // Late arrivals, so each repair replays the boost above out of the
    // ring. Minted by the authority — its seq is the one the session is
    // waiting for — and held back a couple of ticks before delivery, which
    // is what makes them late rather than hand-numbered fiction.
    for (let i = 0; i < 4; i++) {
      await r.run(300);
      r.answerChecks();
      const arrival = r.authority.apply(Ev.join, dogPayload(BigInt(0x96a0 + i)));
      await r.run(100);
      r.deliver([arrival]);
      expect(await r.until(() => r.state.seq === r.authority.seq, 2000)).toBe(true);
    }
    await r.run(600);
    r.answerChecks();
    await r.run(600);

    expect(r.state.rollbacks, "repairs performed").toBeGreaterThan(0);
    expect(r.state.mismatches, "hash mismatches").toBe(mismatchesBefore);
  });
});

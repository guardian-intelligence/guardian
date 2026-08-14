// Where the replica stands relative to the authority, measured against the
// authority's real tick rather than against the client's model of it. The
// rig owns both worlds, so this is ground truth: the only thing between
// them is the fake network, and the fake network is the variable.
//
// Everything here samples AFTER the clock has acquired. That is not a
// detail — from a standing start the replica begins level with the
// authority and the clock walks it back to the lead over about twelve
// seconds, so a measurement taken during those seconds reads whatever
// moment it happened to catch and looks like a steady state.

import { describe, expect, it } from "vitest";
import { bringTheDogIn, rig } from "./wasm.ts";

/** One tick at the fixture park's 24Hz. */
const TICK_MS = 1000 / 24;
/** The clock walks to its lead at a bounded slew; this is past the end of that. */
const ACQUIRE_MS = 15_000;
const SETTLED_MS = 5_000;

type Settled = {
  /** Ticks behind the authority, sampled only after the clock has acquired. */
  readonly trail: number[];
  /** What the client believes its deficit is, once settled. Negative is ahead. */
  readonly believed: number[];
};

async function measureSettled(rttMs: number): Promise<Settled> {
  const dog = 0x9501n;
  const r = await rig({ role: "player", myDog: dog, rttMs, checkMs: 150 });
  await r.establish();
  await bringTheDogIn(r, dog);

  const trail: number[] = [];
  const believed: number[] = [];
  const startedAt = r.authority.tick;
  for (let t = 0; t < ACQUIRE_MS + SETTLED_MS; t += 8) {
    r.harness.clock.advance(8);
    r.core.pump();
    r.answerChecks();
    // The authority keeps its own time, stepping on the wall clock at the
    // park's rate rather than following the replica. That is what makes
    // the gap below a measurement instead of a definition.
    const should = startedAt + BigInt(Math.floor(t / TICK_MS));
    while (r.authority.tick < should) r.authority.step();
    await r.harness.settle();
    if (t >= ACQUIRE_MS) {
      trail.push(Number(r.authority.tick - r.core.state.tick));
      believed.push(r.core.clockErrorTicks());
    }
  }
  return { trail, believed };
}

const median = (xs: number[]): number => [...xs].sort((a, b) => a - b)[Math.floor(xs.length / 2)]!;

describe("the replica's distance from the authority", () => {
  it(
    "settles, and settles in the same place whatever the STEADY round trip",
    { timeout: 180_000 },
    async () => {
      // Two claims, and the second is the one that matters to a player. The
      // clock has to finish acquiring at all — a controller that never
      // closes leaves the replica somewhere nobody chose. And where it
      // settles must not depend on how long a STEADY trip takes, because
      // the trip is compensated when the clock samples it: if this ever
      // starts growing with plain latency, everyone on a slow link is
      // watching an older world than the one they are playing in.
      //
      // Steady is the operative word. A jittery or lossy link legitimately
      // settles further back — the model turns conservative when its
      // samples disagree — and that is measured separately rather than
      // gated here, because how much further is a design question nobody
      // has ruled on yet.
      const fast = await measureSettled(0);
      const slow = await measureSettled(240);

      expect(Math.abs(median(fast.believed)), "deficit still open at 0ms").toBeLessThan(1);
      expect(Math.abs(median(slow.believed)), "deficit still open at 240ms").toBeLessThan(1);
      expect(
        Math.abs(median(slow.trail) - median(fast.trail)),
        `settled trail moved with the round trip: ${median(fast.trail)} ticks at 0ms, ` +
          `${median(slow.trail)} at 240ms`,
      ).toBeLessThanOrEqual(1);
    },
  );
});

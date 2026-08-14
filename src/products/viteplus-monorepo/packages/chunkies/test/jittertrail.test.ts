import { describe, expect, it } from "vitest";
import { bringTheDogIn, decodeCheck, rig, seededRandom32 } from "@guardian/chunkies-testkit";

const TICK_MS = 1000 / 24;
/** Past the end of the clock's walk to its lead; anything earlier reads the acquisition. */
const ACQUIRE_MS = 15_000;
const SETTLED_MS = 8_000;
const lines: string[] = [];

type Net = {
  readonly name: string;
  readonly rttMs: number;
  readonly jitterMs: number;
  readonly lossPct: number;
  /** A total blackout, taken once mid-measurement. */
  readonly hiccupMs?: number;
};

/**
 * Answers checks the way a network does rather than the way a schedule
 * does: each verdict takes its own trip, some never arrive, and a blackout
 * swallows whatever is in flight. The clock builds its model of the
 * authority out of exactly these samples, so this is the input that
 * decides where the replica ends up sitting.
 */
function answerOver(r: Awaited<ReturnType<typeof rig>>, net: Net, rand: () => number) {
  let cursor = 0;
  let blackoutUntil = -1;
  return {
    startBlackout(atMs: number) {
      blackoutUntil = atMs + (net.hiccupMs ?? 0);
    },
    pump(nowMs: number) {
      const sent = r.harness.transport.sentDatagrams;
      for (; cursor < sent.length; cursor++) {
        if (nowMs < blackoutUntil) continue;
        if (rand() % 10_000 < net.lossPct * 100) continue;
        const check = decodeCheck(sent[cursor]!);
        const jitter = ((rand() % 1000) / 1000 - 0.5) * net.jitterMs;
        const half = Math.max(0, (net.rttMs + jitter) / 2);
        r.harness.clock.schedule(() => {
          const verdict = r.authority.verdict(check);
          r.harness.clock.schedule(() => {
            if (nowMs < blackoutUntil) return;
            try {
              r.harness.transport.deliverDatagram(verdict);
            } catch {
              // Connection gone; a lost datagram is the contract.
            }
          }, half);
        }, half);
      }
    },
  };
}

async function traceTrail(net: Net, ms: number): Promise<string> {
  const dog = 0x9902n;
  const rand = seededRandom32(7);
  const r = await rig({ role: "player", myDog: dog, checkMs: 150 });
  await r.establish();
  await bringTheDogIn(r, dog);
  const link = answerOver(r, net, rand);
  const startedAt = r.authority.tick;
  const out: string[] = [];
  let next = 0;
  for (let t = 0; t < ms; t += 8) {
    r.harness.clock.advance(8);
    r.pump();
    const should = startedAt + BigInt(Math.floor(t / TICK_MS));
    while (r.authority.tick < should) r.authority.step();
    link.pump(t);
    await r.harness.settle();
    if (t >= next) {
      out.push(`${(t / 1000).toFixed(0)}s:${Number(r.authority.tick - r.state.tick)}`);
      next = t + 5000;
    }
  }
  return out.join(" ");
}

async function settledTrail(net: Net): Promise<number[]> {
  const dog = 0x9901n;
  const rand = seededRandom32(7);
  const r = await rig({ role: "player", myDog: dog, checkMs: 150 });
  await r.establish();
  await bringTheDogIn(r, dog);

  const net_ = answerOver(r, net, rand);
  const trail: number[] = [];
  const startedAt = r.authority.tick;
  for (let t = 0; t < ACQUIRE_MS + SETTLED_MS; t += 8) {
    r.harness.clock.advance(8);
    r.pump();
    const should = startedAt + BigInt(Math.floor(t / TICK_MS));
    while (r.authority.tick < should) r.authority.step();
    net_.pump(t);
    if (net.hiccupMs && t === 6000) net_.startBlackout(t);
    await r.harness.settle();
    if (t >= ACQUIRE_MS) trail.push(Number(r.authority.tick - r.state.tick));
  }
  return trail;
}

const median = (xs: number[]): number => [...xs].sort((a, b) => a - b)[Math.floor(xs.length / 2)]!;

describe("trail under real network shapes", () => {
  it(
    "measures where the replica settles on each leg's conditions",
    { timeout: 300_000 },
    async () => {
      const legs: Net[] = [
        { name: "clean", rttMs: 0, jitterMs: 0, lossPct: 0 },
        { name: "home-wifi-steady", rttMs: 40, jitterMs: 30, lossPct: 0.5 },
        { name: "home-wifi-dropout", rttMs: 40, jitterMs: 30, lossPct: 0.5, hiccupMs: 600 },
        { name: "bad-lte", rttMs: 120, jitterMs: 60, lossPct: 2, hiccupMs: 800 },
      ];
      let clean = 0;
      const impaired: [string, number][] = [];
      for (const leg of legs) {
        const trail = await settledTrail(leg);
        const sorted = [...trail].sort((a, b) => a - b);
        if (leg.rttMs === 0 && leg.jitterMs === 0) clean = median(trail);
        else impaired.push([leg.name, median(trail)]);
        lines.push(
          `${leg.name.padEnd(18)} rtt ${String(leg.rttMs).padStart(3)} jitter ${String(leg.jitterMs).padStart(2)} ` +
            `loss ${leg.lossPct}% -> trail median ${median(trail)} ` +
            `p10 ${sorted[Math.floor(sorted.length * 0.1)]} p90 ${sorted[Math.floor(sorted.length * 0.9)]} ` +
            `min ${sorted[0]} max ${sorted.at(-1)}`,
        );
      }
      // A level or a slope? The whole point of sampling late is lost if
      // the number is still moving when we sample it.
      lines.push(
        "clean over time:      " +
          (await traceTrail({ name: "clean", rttMs: 0, jitterMs: 0, lossPct: 0 }, 45000)),
      );
      lines.push(
        "home-wifi over time:  " +
          (await traceTrail({ name: "hw", rttMs: 40, jitterMs: 30, lossPct: 0.5 }, 45000)),
      );
      const { writeFileSync } = await import("node:fs");
      writeFileSync("/tmp/jitter.txt", lines.join("\n"));

      // The invariant worth holding, short of a ruling on the right size:
      // a noisy link must never leave the replica with LESS margin than a
      // clean one. The clock builds its model from verdict samples, and a
      // filter that answered noise by moving the replica CLOSER to the
      // authority would hand out less room exactly where more is needed.
      // How much more is a design question nobody has settled; that it is
      // never less is not.
      expect(clean, "a clean link settles at the lead").toBeGreaterThan(0);
      for (const [name, m] of impaired) {
        expect(
          m,
          `${name} settled closer to the authority than a clean link`,
        ).toBeGreaterThanOrEqual(clean);
      }
    },
  );
});

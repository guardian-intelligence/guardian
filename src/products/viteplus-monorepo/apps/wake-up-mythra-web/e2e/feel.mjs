// The play-test: a scripted player on a degraded path, scored on how the
// game FEELS rather than whether it agrees with the authority. The
// correctness drill (degradation.mjs) asserts the replica converges; this
// one asserts it converges without rewinding the world, teleporting your
// dog, or stalling the park — the regressions that reached a live player
// while every hash check stayed green.
//
// It reads the same per-frame record the production jank beacon counts
// (src/game/jank.ts, behind ?probe=1), so a number this fails on locally is
// the number wum.jank reports from real sessions.
//
// Run against a proxied dev stack:
//
//   bazelisk run //src/services/mythrad/netsim &
//   WUM_DEV_PUBLIC_ADDR=127.0.0.1:14433 scripts/wum-dev.sh &
//   node e2e/feel.mjs
//
// Legs are network profiles, played identically:
//   clean      no impairment: the floor. Anything here is not the network.
//   home-wifi  40ms/30ms jitter/0.5% loss — an ordinary living room, and
//              the leg the thresholds gate on.
//   bad-lte    120ms/60ms/2% — a phone on a bad cell, reported not gated.
import { chromium } from "playwright";

const APP = process.env.WUM_APP_URL ?? "http://127.0.0.1:4254";
const CTL = process.env.NETSIM_CONTROL ?? "http://127.0.0.1:14434";
const PLAYER = process.env.WUM_FEEL_PLAYER ?? "feel-tester";
/** Play cycles per leg. One cycle is about 5.5s of scripted input. */
const CYCLES = Number(process.env.WUM_FEEL_CYCLES ?? 6);
/** Click positions come from this, so every leg and every run plays the same script. */
const SEED = Number(process.env.WUM_FEEL_SEED ?? 7);
/** Frames the sim needs to settle after an impairment change, before scoring. */
const SETTLE_MS = 4000;

// `hiccupMs` is a total dropout, taken once per play cycle at the moment
// the dog is mid-walk with an intent unanswered. Real wifi does this — an
// AP roam, a microwave, a neighbour's burst — and a steady impairment
// alone never produces the state a reconciliation has to correct.
const LEGS = [
  {
    name: "clean",
    impair: { latency_ms: 0, jitter_ms: 0, loss_pct: 0 },
    hiccupMs: 0,
    // An unimpaired path has no excuse for showing the world at a tick it
    // already passed, so the rewind check applies here even though the
    // feel thresholds do not.
    gates: ["live", "backwardTicks"],
  },
  {
    name: "home-wifi",
    // 600ms is an access-point roam: the commonest dropout a living room
    // produces, and long enough that the replica notices — shorter gaps
    // disappear into QUIC's retransmits.
    impair: { latency_ms: 40, jitter_ms: 30, loss_pct: 0.5 },
    hiccupMs: 600,
    gates: ["live", "backwardTicks", "ownDogJumps", "rafGapP99Ms", "stallRuns"],
  },
  {
    name: "bad-lte",
    impair: { latency_ms: 120, jitter_ms: 60, loss_pct: 2 },
    hiccupMs: 800,
    // Reported, never gated: a bad cell is allowed to feel bad, and the
    // numbers are here to watch the trend, not to block on it.
    gates: [],
  },
];

// What "plays well" means, gated on the home-wifi leg only: a bad cell is
// allowed to feel bad, a living room is not. Every threshold is a property
// of frames drawn, and each one is here because its absence is a thing a
// player says out loud.
//
//   backwardTicks   "it jumped back" — the world un-happening on screen.
//                   Rollback is legitimate; showing it is not, so any
//                   frame-visible rewind fails.
//   ownDogJumps     "my dog teleported" — displacement no walk explains
//                   (>0.5 cell in a frame; see jank.ts). One per ~1800
//                   frames is a resync landing; a stream of them is not.
//   rafGapP99Ms     "it hitches" — the frame budget, not the network.
//   stallRuns       "it froze" — the tick standing still for a quarter
//                   second while frames keep drawing. One per leg absorbs a
//                   single resync; more is a session that keeps stopping.
const GATES = {
  backwardTicks: 0,
  ownDogJumps: 1,
  rafGapP99Ms: 100,
  stallRuns: 1,
};

/** Tick stillness that counts as a stall, matching FREEZE_MS in jank.ts. */
const STALL_MS = 250;
/** Own-dog displacement that cannot be walking, matching JUMP_CELLS in jank.ts. */
const JUMP_CELLS = 0.5;
const Q16 = 65536;

const ctl = (path) => fetch(`${CTL}${path}`, { method: "POST" });
const impair = (q) =>
  ctl(`/impair?${new URLSearchParams({ ...q, seed: "7" })}`).catch((e) => {
    throw new Error(`netsim control unreachable at ${CTL}: ${e.message}`);
  });

function quantile(sorted, q) {
  if (sorted.length === 0) return 0;
  const at = Math.min(sorted.length - 1, Math.max(0, Math.ceil(q * sorted.length) - 1));
  return sorted[at];
}

// The scorecard: every figure derived from the frames the renderer drew,
// so each one is answerable as "what a player would have seen".
function score(records) {
  const gaps = [];
  const ownDisp = [];
  const otherDisp = [];
  let backwardTicks = 0;
  let tickBursts = 0;
  let ownDogJumps = 0;
  let stallRuns = 0;
  let staticRun = 0;
  let longestStaticRun = 0;
  let tickChangedAt = records[0]?.t ?? 0;
  let stalled = false;
  const lastSeen = new Map();

  for (let i = 0; i < records.length; i++) {
    const r = records[i];
    const prev = records[i - 1];
    if (prev) {
      const gap = r.t - prev.t;
      gaps.push(gap);
      const dTick = r.tick - prev.tick;
      if (dTick < 0) backwardTicks++;
      if (dTick > 2) tickBursts++;
      if (r.mine && prev.mine) {
        const d = Math.hypot(r.myX - prev.myX, r.myY - prev.myY) / Q16;
        ownDisp.push(d);
        if (d > JUMP_CELLS && gap <= 50) ownDogJumps++;
        // A presented dog that never moves while the world advances is the
        // other half of "frozen": the tick climbs, the screen does not.
        if (dTick > 0 && r.myX === prev.myX && r.myY === prev.myY) {
          staticRun++;
          longestStaticRun = Math.max(longestStaticRun, staticRun);
        } else {
          staticRun = 0;
        }
      }
      if (r.tick !== prev.tick) {
        tickChangedAt = r.t;
        stalled = false;
      } else if (!stalled && r.t - tickChangedAt > STALL_MS) {
        stalled = true;
        stallRuns++;
      }
    }
    for (let d = 0; d + 2 < r.dogs.length; d += 3) {
      const id = r.dogs[d];
      const x = r.dogs[d + 1];
      const y = r.dogs[d + 2];
      const was = lastSeen.get(id);
      if (was && was.frame === i - 1) {
        otherDisp.push(Math.hypot(x - was.x, y - was.y) / Q16);
      }
      lastSeen.set(id, { x, y, frame: i });
    }
  }

  const sortedGaps = [...gaps].sort((a, b) => a - b);
  const sortedOwn = [...ownDisp].sort((a, b) => a - b);
  const sortedOther = [...otherDisp].sort((a, b) => a - b);
  return {
    frames: records.length,
    seconds: records.length > 1 ? (records[records.length - 1].t - records[0].t) / 1000 : 0,
    rafGapP99Ms: quantile(sortedGaps, 0.99),
    rafGapMaxMs: sortedGaps[sortedGaps.length - 1] ?? 0,
    tickBursts,
    backwardTicks,
    ownDispP99Cells: quantile(sortedOwn, 0.99),
    ownDispMaxCells: sortedOwn[sortedOwn.length - 1] ?? 0,
    ownDogJumps,
    stallRuns,
    longestStaticRun,
    otherDispP99Cells: quantile(sortedOther, 0.99),
    otherDogJumps: otherDisp.filter((d) => d > JUMP_CELLS).length,
  };
}

// A failing number is a lead, not a verdict, so a failing leg prints the
// frames its worst jumps happened on: what the tick, the interpolation
// phase and the population were doing on either side says which repair
// path produced them.
function worstJumps(records, count) {
  const found = [];
  let lastAt = -Infinity;
  for (let i = 1; i < records.length; i++) {
    const r = records[i];
    const prev = records[i - 1];
    if (!r.mine || !prev.mine) continue;
    const d = Math.hypot(r.myX - prev.myX, r.myY - prev.myY) / Q16;
    if (d <= JUMP_CELLS) continue;
    // One correction spread over the frames the smoother took to chase it
    // is one incident, not four.
    if (i - lastAt <= 8) {
      const last = found[found.length - 1];
      if (last && d > last.cells) {
        last.cells = d;
        last.at = i;
      }
    } else {
      found.push({ at: i, cells: d });
    }
    lastAt = i;
  }
  found.sort((a, b) => b.cells - a.cells);
  return found.slice(0, count).map(({ at, cells }) => {
    const frame = (i) => {
      const r = records[i];
      if (!r) return "        —";
      return (
        `        t=${(r.t - records[0].t).toFixed(0)}ms tick=${r.tick}` +
        ` phase=${(r.phaseQ16 / Q16).toFixed(2)}` +
        ` pos=(${(r.myX / Q16).toFixed(3)},${(r.myY / Q16).toFixed(3)})` +
        ` dogs=${r.dogCount}${r.mine ? "" : " [ours absent]"}`
      );
    };
    return [
      `      jump ${cells.toFixed(3)} cells at frame ${at}:`,
      frame(at - 2),
      frame(at - 1),
      frame(at),
      frame(at + 1),
    ].join("\n");
  });
}

function fmt(card) {
  const n = (v, d = 2) => (typeof v === "number" ? v.toFixed(d) : String(v));
  return [
    `frames=${card.frames} over ${n(card.seconds, 1)}s`,
    `rAF p99=${n(card.rafGapP99Ms, 1)}ms max=${n(card.rafGapMaxMs, 1)}ms`,
    `tick bursts>2=${card.tickBursts} backward=${card.backwardTicks}`,
    `own dog p99=${n(card.ownDispP99Cells, 3)} max=${n(card.ownDispMaxCells, 3)} cells jumps=${card.ownDogJumps}`,
    `stalls=${card.stallRuns} longest static run=${card.longestStaticRun}f`,
    `other dogs p99=${n(card.otherDispP99Cells, 3)} jumps=${card.otherDogJumps}`,
  ].join("\n    ");
}

function gateFailures(card, session, gates) {
  const fails = [];
  // A scorecard from a session that journaled nothing is not evidence of
  // anything: no intents landed, so nothing was predicted, reconciled or
  // repaired, and every figure below would be a green zero.
  if (gates.includes("live") && session.events === 0) {
    fails.push("leg journaled no events — the session was not live");
  }
  if (gates.includes("backwardTicks") && card.backwardTicks > GATES.backwardTicks) {
    fails.push(`backward ticks ${card.backwardTicks} > ${GATES.backwardTicks}`);
  }
  if (gates.includes("ownDogJumps") && card.ownDogJumps > GATES.ownDogJumps) {
    fails.push(`own-dog jumps ${card.ownDogJumps} > ${GATES.ownDogJumps}`);
  }
  if (gates.includes("rafGapP99Ms") && card.rafGapP99Ms > GATES.rafGapP99Ms) {
    fails.push(`rAF gap p99 ${card.rafGapP99Ms.toFixed(1)}ms > ${GATES.rafGapP99Ms}ms`);
  }
  if (gates.includes("stallRuns") && card.stallRuns > GATES.stallRuns) {
    fails.push(`stall runs ${card.stallRuns} > ${GATES.stallRuns}`);
  }
  return fails;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// The inputs. A player moving somewhere, holding boost to get there, and
// the mash the live report came in on: boost tapped faster than the journal
// can answer, which is where own-intent prediction and the reject lane meet.
async function playInputs(page, box, cycles, hiccupMs, rand) {
  for (let c = 0; c < cycles; c++) {
    // Walk somewhere, then lose the network WHILE walking. The order is the
    // whole point: the dropout has to land on a dog that is mid-path with an
    // intent still unanswered, because that is the state a reconciliation
    // has to correct — and correcting it is where a player sees a teleport.
    // A dropout timed independently of the input mostly lands on a dog
    // standing still, which corrects to nothing and proves nothing.
    await page.mouse.click(
      box.x + box.width * (0.2 + rand() * 0.6),
      box.y + box.height * (0.2 + rand() * 0.6),
    );
    await sleep(400);
    if (hiccupMs > 0) await ctl(`/silence?ms=${hiccupMs}`);
    await sleep(hiccupMs + 600);

    await page.dispatchEvent("#boost", "pointerdown");
    await sleep(1000);
    await page.dispatchEvent("#boost", "pointerup");
    await sleep(600);

    // The mash the live report came in on: boost tapped faster than the
    // journal can answer, so prediction and the reject lane overlap.
    for (let i = 0; i < 10; i++) {
      await page.dispatchEvent("#boost", "pointerdown");
      await sleep(70);
      await page.dispatchEvent("#boost", "pointerup");
      await sleep(70);
    }
    await sleep(800);
  }
}

/** xorshift32, so every leg plays the identical script. */
function seeded(seed) {
  let s = seed >>> 0 || 1;
  return () => {
    s ^= s << 13;
    s >>>= 0;
    s ^= s >>> 17;
    s ^= s << 5;
    s >>>= 0;
    return s / 0x1_0000_0000;
  };
}

const browser = await chromium.launch({ args: ["--enable-features=WebTransport"] });
const page = await browser.newPage();
page.on("pageerror", (e) => console.error(`page error: ${e.message}`));
await page.goto(`${APP}/?probe=1`, { waitUntil: "domcontentloaded" });
await page.waitForFunction(
  () => document.getElementById("status")?.textContent === "CONNECTED",
  undefined,
  { timeout: 30000 },
);

// Sign in through the real code flow: the dev issuer's form stands in for
// the broker, and the popup relays back exactly as Google's would.
const [popup] = await Promise.all([page.waitForEvent("popup"), page.click("#signin")]);
await popup.waitForSelector("input[name=u]", { timeout: 15000 });
await popup.fill("input[name=u]", PLAYER);
await popup.click("button");
await page.waitForFunction(
  () => document.getElementById("role")?.textContent?.startsWith("player"),
  undefined,
  { timeout: 30000 },
);
// Which modules produced these numbers. A scorecard that cannot name the
// build it scored is a number without a subject — and this stack hot-swaps
// the park module underneath a running session by design.
const info = await (await fetch(`${APP}/wt-info`, { cache: "no-store" })).json();
console.log(
  `signed in as ${PLAYER}; playing ${LEGS.length} legs of ${CYCLES} cycles` +
    ` against park ${info.parkWasm} / client ${info.clientWasm}`,
);

const canvas = await page.waitForSelector("#grid");
const box = await canvas.boundingBox();
const results = [];

for (const leg of LEGS) {
  await impair(leg.impair);
  await sleep(SETTLE_MS);
  const [before, diagBefore] = await page.evaluate(() => [
    globalThis.__mythraProbe.counters(),
    { ...globalThis.__mythraDiag },
  ]);
  await page.evaluate(() => globalThis.__mythraProbe.drain());

  await playInputs(page, box, CYCLES, leg.hiccupMs, seeded(SEED));

  const { records, dropped, after, diagAfter } = await page.evaluate(() => ({
    records: globalThis.__mythraProbe.drain(),
    dropped: globalThis.__mythraProbe.dropped,
    after: globalThis.__mythraProbe.counters(),
    diagAfter: { ...globalThis.__mythraDiag },
  }));
  const session = {
    events: diagAfter.events - diagBefore.events,
    rollbacks: diagAfter.rollbacks - diagBefore.rollbacks,
    resyncs: diagAfter.resyncs - diagBefore.resyncs,
    mismatches: diagAfter.mismatches - diagBefore.mismatches,
    rejects: diagAfter.rejects - diagBefore.rejects,
  };
  const card = score(records);
  const fails = gateFailures(card, session, leg.gates);
  results.push({ leg, card, session, fails });

  console.log(`\n[${leg.name}] ${JSON.stringify(leg.impair)} hiccup=${leg.hiccupMs}ms x${CYCLES}`);
  console.log(`    ${fmt(card)}`);
  // What the session actually went through. A leg that never rolled back or
  // resynced did not test the repair paths, whatever its scorecard says.
  console.log(
    `    session: events=${session.events} rollbacks=${session.rollbacks}` +
      ` resyncs=${session.resyncs} mismatches=${session.mismatches} rejects=${session.rejects}`,
  );
  // The page's own counters over the same frames. They feed wum.jank in
  // production, so a disagreement here means the beacon is lying.
  console.log(
    `    beacon: long=${after.longFrames - before.longFrames}` +
      ` backward=${after.backwardTicks - before.backwardTicks}` +
      ` jumps=${after.ownDogJumps - before.ownDogJumps}` +
      ` freezes=${after.freezeRuns - before.freezeRuns}` +
      (dropped ? ` (ring dropped ${dropped})` : ""),
  );
  if (leg.gates.length > 0) {
    console.log(`    gate: ${fails.length === 0 ? "PASS" : `FAIL — ${fails.join("; ")}`}`);
    if (fails.length > 0) {
      const evidence = worstJumps(records, 3);
      if (evidence.length > 0) console.log(evidence.join("\n"));
    }
  }
}

await impair({ latency_ms: 0, jitter_ms: 0, loss_pct: 0 });
await browser.close();

const failed = results.filter((r) => r.fails.length > 0);
console.log(
  failed.length === 0 ? "\nFEEL OK" : `\nFEEL FAILED: ${failed.map((r) => r.leg.name).join(", ")}`,
);
process.exit(failed.length === 0 ? 0 : 1);

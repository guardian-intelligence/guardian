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
// For an acceptance measurement, pin the candidate build and repeat, so the
// numbers name a build and carry their spread:
//
//   WUM_FEEL_CANDIDATE=<client word> WUM_FEEL_RUNS=3 node e2e/feel.mjs
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
/**
 * Repeats of the whole leg sequence. Acceptance takes three, because one
 * run of a game on a lossy path is an anecdote.
 */
const RUNS = Number(process.env.WUM_FEEL_RUNS ?? 1);
/**
 * The client module word this measurement is FOR. The park hot-swaps
 * modules under a live session, so without a pin a run can silently score
 * a build nobody proposed. Set it for an acceptance run:
 *
 *   WUM_FEEL_CANDIDATE=43e29e90 WUM_FEEL_RUNS=3 node e2e/feel.mjs
 *
 * Unset, the harness scores whatever is live and says so — useful for a
 * lead, never for an acceptance.
 */
const CANDIDATE = process.env.WUM_FEEL_CANDIDATE ?? null;

/** What the park is serving right now. Re-read before every leg. */
const modules = async () => (await fetch(`${APP}/wt-info`, { cache: "no-store" })).json();

// `hiccupMs` is a total dropout, taken once per play cycle at the moment
// the dog is mid-walk with an intent unanswered. Real wifi does this — an
// AP roam, a microwave, a neighbour's burst — and a steady impairment
// alone never produces the state a reconciliation has to correct.
const LEGS = [
  {
    name: "clean",
    impair: { latency_ms: 0, jitter_ms: 0, loss_pct: 0 },
    hiccupMs: 0,
    // An unimpaired path has even less excuse than a degraded one for
    // showing the world at a tick it already passed, or for moving a dog
    // somewhere no walk could take it — and no excuse at all for needing a
    // snapshot. The timing thresholds stay off: frame budget and stalls are
    // the network's business, and there is no network here to blame.
    gates: ["live", "backwardTicks", "ownDogJumps", "noResyncs", "lateRepairs"],
  },
  {
    // A living room with no dropout. Every jump here is prediction-class
    // jank, because there is no repair to attribute one to.
    name: "home-wifi-steady",
    impair: { latency_ms: 40, jitter_ms: 30, loss_pct: 0.5 },
    hiccupMs: 0,
    gates: ["live", "backwardTicks", "ownDogJumps", "rafGapP99Ms", "stallRuns"],
  },
  {
    // The same living room, plus a 600ms access-point roam taken while the
    // dog is mid-walk with an intent unanswered. This is the best teleport
    // detector we have, but the repairs it provokes are honest ones and the
    // lever for their size is the staged lag/jitter-buffer work, not this
    // branch. So the gate is attribution, not count: a jump must have a
    // repair span beside it, and an unexplained one still fails.
    name: "home-wifi-dropout",
    impair: { latency_ms: 40, jitter_ms: 30, loss_pct: 0.5 },
    hiccupMs: 600,
    gates: ["live", "backwardTicks", "jumpsAttributable", "rafGapP99Ms", "stallRuns"],
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

// What "plays well" means. A bad cell is allowed to feel bad; a living room
// is not. Every threshold is a property of frames drawn, and each one is
// here because its absence is a thing a player says out loud. Which legs
// carry which check is on the legs above.
//
//   backwardTicks       "it jumped back" — the world un-happening on
//                       screen. Rollback is legitimate; showing it is not.
//   ownDogJumps         "my dog teleported" — displacement no walk explains
//                       (>0.5 cell in a frame; see jank.ts). Gated where
//                       there is no dropout to blame, so a jump means
//                       prediction, not repair.
//   jumpsAttributable   the same teleport, judged differently where a
//                       dropout IS in play: a jump must have a repair span
//                       beside it. Honest repairs cost what they cost; a
//                       jump nothing explains is still a bug.
//   noResyncs           an unimpaired path should never need a snapshot.
//                       Reasons are reported separately, because "check
//                       aged out" and "world hash mismatch" are two
//                       different failures wearing one number.
//   rafGapP99Ms         "it hitches" — the frame budget, not the network.
//   stallRuns           "it froze" — the tick standing still for a quarter
//                       second while frames keep drawing.
//   lateRepairs         a repair reaching back further than the lead can
//                       explain. On an unimpaired path an event is late by
//                       the lead and no more, so a repair that has to reach
//                       back further is repairing something this replica did
//                       to itself — an event stepped over rather than an
//                       event that arrived behind. Counting repairs cannot
//                       see that (at zero latency every own action costs
//                       one, by design); their DEPTH can.
const GATES = {
  backwardTicks: 0,
  ownDogJumps: 1,
  rafGapP99Ms: 100,
  stallRuns: 1,
  /**
   * Ticks a repair may reach back on an unimpaired path. Measured rather
   * than chosen: across a full pass of clean legs the lateness was 1 tick
   * 31 times, 2 ticks 40 times, 3 ticks 6 times and 4 ticks once, so four
   * is the observed ceiling of an honest late event at zero latency.
   */
  lateRepairTicks: 4,
};

/**
 * How close a repair has to be to a jump for the jump to be its doing. A
 * correction lands within a frame or two of the repair that caused it; half
 * a second is generous, which is the right direction to be generous in when
 * the alternative is blaming an honest repair for prediction jank.
 */
const ATTRIBUTION_MS = 500;
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
    for (const dog of r.dogs) {
      const was = lastSeen.get(dog.id);
      if (was && was.frame === i - 1) {
        otherDisp.push(Math.hypot(dog.xq - was.x, dog.yq - was.y) / Q16);
      }
      lastSeen.set(dog.id, { x: dog.xq, y: dog.yq, frame: i });
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
/**
 * Jumps as incidents rather than frames: one correction spread over the
 * frames the smoother took to chase it is one thing that happened.
 */
function jumpIncidents(records) {
  const found = [];
  let lastAt = -Infinity;
  for (let i = 1; i < records.length; i++) {
    const r = records[i];
    const prev = records[i - 1];
    if (!r.mine || !prev.mine) continue;
    const d = Math.hypot(r.myX - prev.myX, r.myY - prev.myY) / Q16;
    if (d <= JUMP_CELLS) continue;
    if (i - lastAt <= 8) {
      const last = found[found.length - 1];
      if (last && d > last.cells) {
        last.cells = d;
        last.at = i;
        last.t = r.t;
      }
    } else {
      found.push({ at: i, t: r.t, cells: d });
    }
    lastAt = i;
  }
  return found;
}

/**
 * Which jumps a repair explains. A rollback, resync or restore beside a
 * jump makes it the visible cost of a repair the netcode chose to make; a
 * jump with nothing beside it is the world moving our dog for no reason we
 * can name, which is the thing this harness exists to catch.
 */
function attribute(records, spans) {
  const repairs = spans.filter((s) =>
    ["wum.netcode_rollback", "wum.netcode_resync", "wum.netcode_restore"].includes(s.name),
  );
  const attributed = [];
  const unattributed = [];
  for (const jump of jumpIncidents(records)) {
    const near = repairs.find((s) => Math.abs(s.t - jump.t) <= ATTRIBUTION_MS);
    (near ? attributed : unattributed).push({ ...jump, by: near?.name });
  }
  return { attributed, unattributed, repairs: repairs.length };
}

/**
 * The decisive read on a surviving mismatch: was the tick we were asked
 * about one a repair had already rewritten?
 *
 * A check carries the hash the replica held when it asked. If a repair
 * then rewrites that tick, the authority answers about history we have
 * already replaced — true when asked, stale when it lands, and no evidence
 * of divergence. If NO repair touched the tick, the two worlds genuinely
 * disagree about it, which is a different and much worse thing.
 *
 * The rollback span carries how far it reached but not where from, so the
 * range is reconstructed from the frame the replica was drawing when the
 * span fired. That is an inference, and it is why the output below prints
 * the range rather than just the verdict.
 */
function mismatchCorrelation(records, spans) {
  const tickAt = (t) => {
    let best = null;
    for (const r of records) {
      if (r.t <= t && (best === null || r.t > best.t)) best = r;
    }
    return best?.tick ?? null;
  };
  const rollbacks = spans
    .filter((s) => s.name === "wum.netcode_rollback")
    .map((s) => {
      // Absolute ticks if the span carries them, which makes the range a
      // fact; otherwise reconstructed from the frame being drawn, which
      // makes it an inference. Never silently one pretending to be the
      // other — the verdict says which it had.
      const from = s.attrs["wum.from_tick"];
      const to = s.attrs["wum.to_tick"];
      if (from !== undefined && to !== undefined) {
        return { t: s.t, from: Number(from), to: Number(to), measured: true };
      }
      const at = tickAt(s.t);
      const rewound = Number(s.attrs["wum.rewound_ticks"] ?? 0);
      return { t: s.t, from: at === null ? null : at - rewound, to: at, measured: false };
    });
  return spans
    .filter((s) => s.name === "wum.netcode_mismatch")
    .map((s) => {
      const tick = Number(s.attrs["wum.tick"]);
      const covering = rollbacks.filter((r) => r.from !== null && tick >= r.from && tick <= r.to);
      const nearest = rollbacks.reduce(
        (best, r) => (best === null || Math.abs(r.t - s.t) < Math.abs(best.t - s.t) ? r : best),
        null,
      );
      return { tick, covering, nearest, gapMs: nearest === null ? null : s.t - nearest.t };
    });
}

/**
 * The margin every journalled event arrived with, in ticks: how far the
 * replica still had to step before reaching the tick the event was stamped
 * for. Positive means the event beat the replica there; negative means it
 * was already late and a repair was owed.
 *
 * This is the measurement that makes the authority's stamp-to-wire delay a
 * number rather than an estimate — the delay is the replica's trail minus
 * this margin — and the clean leg is where it is readable, because there
 * is no network delay folded in on top of it.
 */
function arrivalMargins(spans) {
  return spans
    .filter((s) => s.name === "wum.netcode_arrived")
    .map((s) => Number(s.attrs["wum.tick"]) - Number(s.attrs["wum.replica_tick"]));
}

/** Resyncs by the reason they gave, so two different causes never read as one number. */
function resyncReasons(spans) {
  const by = new Map();
  for (const s of spans) {
    if (s.name !== "wum.netcode_resync") continue;
    const why = s.attrs["wum.why"];
    by.set(why, (by.get(why) ?? 0) + 1);
  }
  return by;
}

function worstJumps(records, count) {
  const found = jumpIncidents(records);
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

// The repairs the app announced during a leg, in order. The scorecard says
// a rewind happened; these say which repair asked for it and how far back
// it reached, which is the difference between a rollback that overshot and
// a snapshot landing behind the replica.
/**
 * How late each repair's event was, in ticks, for the depth gate.
 *
 * Throws rather than defaulting when the attribute is missing. A gate that
 * reads an absent field gets NaN, every comparison against NaN is false,
 * and it passes forever without ever mentioning that it stopped measuring
 * anything — which is worse than the defect it was watching for. The
 * session core owns these names; if one changes, this stops the run and
 * says so instead of going quietly green.
 */
function repairLateness(spans) {
  return spans
    .filter((s) => s.name === "wum.netcode_rollback")
    .map((s) => {
      const raw = s.attrs["wum.late_ticks"];
      if (raw === undefined) {
        throw new Error(
          "rollback span carries no wum.late_ticks — the lateness gate cannot run. " +
            `Attributes present: ${Object.keys(s.attrs).join(", ")}. ` +
            "If the core's rollback telemetry changed shape, this gate and the " +
            "mismatch correlation both need updating before any verdict is believed.",
        );
      }
      return Number(raw);
    });
}

function repairs(spans) {
  const out = [];
  for (const s of spans) {
    if (s.name === "wum.netcode_rollback") {
      out.push(
        `rollback: late ${s.attrs["wum.late_ticks"]} tick(s),` +
          ` rewound ${s.attrs["wum.rewound_ticks"]}`,
      );
    } else if (s.name === "wum.netcode_resync") {
      out.push(`resync: ${s.attrs["wum.why"]} at seq ${s.attrs["wum.seq"]}`);
    } else if (s.name === "wum.netcode_restore") {
      out.push(`restore: landed at seq ${s.attrs["wum.seq"]} tick ${s.attrs["wum.tick"]}`);
    } else if (s.name === "wum.netcode_teardown") {
      out.push(`teardown: ${s.attrs["wum.why"]}`);
    }
  }
  return out;
}

// A rewind is one frame showing an earlier world than the frame before it.
// Where it lands in the leg, and how far it goes, is what says whether a
// rollback or a snapshot restore produced it.
function rewinds(records) {
  const out = [];
  for (let i = 1; i < records.length; i++) {
    const back = records[i - 1].tick - records[i].tick;
    if (back <= 0) continue;
    out.push(
      `      rewind ${back} tick(s) at ${((records[i].t - records[0].t) / 1000).toFixed(1)}s` +
        ` into the leg: tick ${records[i - 1].tick} → ${records[i].tick},` +
        ` pos (${(records[i - 1].myX / Q16).toFixed(2)},${(records[i - 1].myY / Q16).toFixed(2)})` +
        ` → (${(records[i].myX / Q16).toFixed(2)},${(records[i].myY / Q16).toFixed(2)})`,
    );
  }
  return out;
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

function gateFailures(card, session, gates, evidence) {
  const fails = [];
  if (gates.includes("jumpsAttributable") && evidence.attribution.unattributed.length > 0) {
    const sizes = evidence.attribution.unattributed
      .map((j) => `${j.cells.toFixed(2)} cells`)
      .join(", ");
    fails.push(
      `${evidence.attribution.unattributed.length} own-dog jump(s) with no repair beside them` +
        ` (${sizes}); ${evidence.attribution.attributed.length} of ${evidence.attribution.repairs} repairs accounted for the rest`,
    );
  }
  if (gates.includes("noResyncs") && evidence.resyncs.size > 0) {
    const named = [...evidence.resyncs].map(([why, n]) => `${why} x${n}`).join(", ");
    fails.push(`resynced on an unimpaired path: ${named}`);
  }
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
  if (gates.includes("lateRepairs")) {
    const deep = evidence.lateness.filter((n) => n > GATES.lateRepairTicks);
    if (deep.length > 0) {
      fails.push(
        `${deep.length} repair(s) reached back further than the lead explains` +
          ` (${deep.join(", ")} ticks late, ceiling ${GATES.lateRepairTicks})`,
      );
    }
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
const info = await modules();
console.log(
  `signed in as ${PLAYER}; playing ${LEGS.length} legs of ${CYCLES} cycles` +
    ` x ${RUNS} run(s) against park ${info.parkWasm} / client ${info.clientWasm}` +
    (CANDIDATE ? ` (candidate ${CANDIDATE})` : " (no candidate pinned — leads only)"),
);

const canvas = await page.waitForSelector("#grid");
const box = await canvas.boundingBox();
const results = [];

for (let run = 1; run <= RUNS; run++) {
  if (run > 1) {
    // Runs must be independent or the measurement's subject quietly becomes
    // session age: the follow camera clamps at map edges, so as the dog
    // drifts across one long session the same SCREEN click resolves to a
    // different WORLD cell, changing pending-move durations and with them
    // every jump count. A reload restores the same starting situation; the
    // signed-in identity survives it, so only the connect waits repeat.
    await page.reload({ waitUntil: "domcontentloaded" });
    await page.waitForFunction(
      () => document.getElementById("status")?.textContent === "CONNECTED",
      undefined,
      { timeout: 30000 },
    );
    await page.waitForFunction(
      () => document.getElementById("role")?.textContent?.startsWith("player"),
      undefined,
      { timeout: 30000 },
    );
  }
  for (const leg of LEGS) {
    const at = await modules();
    // The park hot-swaps modules under a running session by design, so the
    // build can change between legs. An acceptance run pinned to a
    // candidate stops here rather than reporting numbers for whatever
    // happened to be live.
    if (CANDIDATE && at.clientWasm !== CANDIDATE) {
      console.log(
        `\nVOID: client is ${at.clientWasm}, not the candidate ${CANDIDATE}` +
          ` — measurement abandoned at ${leg.name} run ${run}/${RUNS}`,
      );
      await browser.close();
      process.exit(2);
    }
    const tag = `${leg.name} run ${run}/${RUNS} client ${at.clientWasm}`;
    await impair(leg.impair);
    await sleep(SETTLE_MS);
    const [before, diagBefore] = await page.evaluate(() => [
      globalThis.__mythraProbe.counters(),
      { ...globalThis.__mythraDiag },
    ]);
    await page.evaluate(() => {
      globalThis.__mythraProbe.drain();
      globalThis.__mythraProbe.drainSpans();
    });

    await playInputs(page, box, CYCLES, leg.hiccupMs, seeded(SEED));

    // Both loss counters are read BEFORE the drains that reset them.
    // Read after, they are always zero — which is what they were, so a
    // full ring has never once been reported in this harness.
    const { records, spans, dropped, droppedSpans, after, diagAfter } = await page.evaluate(() => {
      const dropped = globalThis.__mythraProbe.dropped;
      const droppedSpans = globalThis.__mythraProbe.droppedSpans;
      return {
        dropped,
        droppedSpans,
        records: globalThis.__mythraProbe.drain(),
        spans: globalThis.__mythraProbe.drainSpans(),
        after: globalThis.__mythraProbe.counters(),
        diagAfter: { ...globalThis.__mythraDiag },
      };
    });
    const session = {
      events: diagAfter.events - diagBefore.events,
      rollbacks: diagAfter.rollbacks - diagBefore.rollbacks,
      resyncs: diagAfter.resyncs - diagBefore.resyncs,
      mismatches: diagAfter.mismatches - diagBefore.mismatches,
      rejects: diagAfter.rejects - diagBefore.rejects,
    };
    const card = score(records);
    const evidence = {
      attribution: attribute(records, spans),
      resyncs: resyncReasons(spans),
      lateness: repairLateness(spans),
    };
    const fails = gateFailures(card, session, leg.gates, evidence);
    results.push({ leg, card, session, evidence, fails, run, client: at.clientWasm });

    console.log(`\n[${tag}] ${JSON.stringify(leg.impair)} hiccup=${leg.hiccupMs}ms x${CYCLES}`);
    console.log(`    ${fmt(card)}`);
    // What the session actually went through. A leg that never rolled back or
    // resynced did not test the repair paths, whatever its scorecard says.
    console.log(
      `    session: events=${session.events} rollbacks=${session.rollbacks}` +
        ` resyncs=${session.resyncs} mismatches=${session.mismatches} rejects=${session.rejects}`,
    );
    // What the journal's events cost to deliver, which is the number the
    // pipeline question turns on. Reported on every leg; the clean one is
    // the one to read, since nothing else is folded into it there.
    const margins = arrivalMargins(spans);
    if (margins.length > 0) {
      const sorted = [...margins].sort((a, b) => a - b);
      const late = margins.filter((m) => m < 0).length;
      console.log(
        `    arrival margin: median ${quantile(sorted, 0.5)} ticks ` +
          `(p10 ${quantile(sorted, 0.1)}, p90 ${quantile(sorted, 0.9)}) over ${margins.length} events; ` +
          `${late} arrived already late`,
      );
    }
    // A surviving mismatch is the one thing that must never be read past,
    // so its correlation prints whenever one happens — not only on a fail.
    if (session.mismatches > 0) {
      for (const m of mismatchCorrelation(records, spans)) {
        const verdict =
          m.covering.length > 0
            ? `REWRITTEN by ${m.covering.length} repair(s)`
            : "NO repair touched this tick — worlds genuinely disagree";
        const near =
          m.nearest === null
            ? "no repair in the leg"
            : `nearest repair ${m.gapMs.toFixed(0)}ms away covering ${m.nearest.from}..${m.nearest.to}` +
              ` (${m.nearest.measured ? "measured" : "reconstructed"})`;
        console.log(`    mismatch at tick ${m.tick}: ${verdict}; ${near}`);
      }
    }
    // Resyncs by reason: two different causes must never read as one number.
    if (evidence.resyncs.size > 0) {
      const named = [...evidence.resyncs].map(([why, n]) => `${why} x${n}`).join(", ");
      console.log(`    resync reasons: ${named}`);
    }
    // How many of the jumps a repair explains, and how many nothing does.
    const { attributed, unattributed } = evidence.attribution;
    if (attributed.length + unattributed.length > 0) {
      console.log(
        `    jumps: ${attributed.length} beside a repair, ${unattributed.length} unexplained`,
      );
    }
    // Each repair, named. A count says the leg repaired something; these say
    // what it thought was wrong and how far back it had to go.
    for (const line of repairs(spans)) console.log(`    ${line}`);
    // The page's own counters over the same frames. They feed wum.jank in
    // production, so a disagreement here means the beacon is lying.
    console.log(
      `    beacon: long=${after.longFrames - before.longFrames}` +
        ` backward=${after.backwardTicks - before.backwardTicks}` +
        ` jumps=${after.ownDogJumps - before.ownDogJumps}` +
        ` freezes=${after.freezeRuns - before.freezeRuns}` +
        (dropped ? ` (ring dropped ${dropped})` : "") +
        (droppedSpans ? ` (SPANS DROPPED ${droppedSpans} — oldest evidence is gone)` : ""),
    );
    if (leg.gates.length > 0) {
      console.log(`    gate: ${fails.length === 0 ? "PASS" : `FAIL — ${fails.join("; ")}`}`);
      if (fails.length > 0) {
        const evidence = [...rewinds(records), ...worstJumps(records, 3)];
        if (evidence.length > 0) console.log(evidence.join("\n"));
      }
    }
  }
}

await impair({ latency_ms: 0, jitter_ms: 0, loss_pct: 0 });
await browser.close();

// Across runs, per leg: the spread is the point. One green run of a game on
// a lossy path says very little, and a metric that swings between runs is a
// finding in itself rather than a number to average away.
if (RUNS > 1) {
  console.log(`\nacross ${RUNS} runs${CANDIDATE ? ` of candidate ${CANDIDATE}` : ""}:`);
  for (const leg of LEGS) {
    const mine = results.filter((r) => r.leg.name === leg.name);
    const list = (pick) => mine.map(pick).join("/");
    console.log(
      `    ${leg.name}: backward ${list((r) => r.card.backwardTicks)}` +
        ` · jumps ${list((r) => r.card.ownDogJumps)}` +
        ` · own max ${mine.map((r) => r.card.ownDispMaxCells.toFixed(2)).join("/")} cells` +
        ` · stalls ${list((r) => r.card.stallRuns)}` +
        ` · rollbacks ${list((r) => r.session.rollbacks)}` +
        ` · resyncs ${list((r) => r.session.resyncs)}` +
        ` · mismatches ${list((r) => r.session.mismatches)}`,
    );
  }
}

const failed = results.filter((r) => r.fails.length > 0);
const names = [...new Set(failed.map((r) => `${r.leg.name} (run ${r.run})`))];
console.log(failed.length === 0 ? "\nFEEL OK" : `\nFEEL FAILED: ${names.join(", ")}`);
process.exit(failed.length === 0 ? 0 : 1);

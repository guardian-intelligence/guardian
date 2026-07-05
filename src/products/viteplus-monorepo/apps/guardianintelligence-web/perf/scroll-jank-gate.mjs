// Runtime performance promotion gate for the letters treatment. Required
// check in the site-gate workflow: fails the build if the letters page's
// scroll-frame timing regresses past the thresholds below.
//
//   vp build && PORT=4179 HOST=127.0.0.1 node .output/server/index.mjs &
//   node perf/scroll-jank-gate.mjs
//
// Thresholds are calibrated against measured baselines (2026-07-05, see
// perf/scroll-jank.mjs history) with headroom for CI runner variance:
// Chromium holds a flat ~16.7ms/frame; Firefox has a known, accepted residual
// cost from the paper-layer compositing (~17-26ms avg, PR #359); WebKit was
// the dominant offender (~55ms avg, 15% of frames >33ms) until the hand-
// filter's feTurbulence numOctaves was cut from 2 to 1. Each threshold sits
// above its engine's fixed-state ceiling and below its broken-state floor.
// Three attempts per engine; the MEDIAN run is compared against budget --
// resistant to a single outlier sample in either direction, unlike a
// best-of-N (which would let one lucky sample from a genuinely regressed
// build slip under budget). Thresholds were picked from measurements on a
// quiet machine; if this flakes on the actual CI runner, re-baseline against
// real runs there rather than loosening blindly.
import { chromium, firefox, webkit } from "playwright";
import { measureScrollJank } from "./lib/scroll-measure.mjs";

const base = process.env.BASE ?? "http://127.0.0.1:4179";
const path = process.env.PAGE ?? "/letters/dear-shovon";
const ATTEMPTS = 3;

const BUDGETS = {
  chromium: { avg: 25, jank: 5 },
  firefox: { avg: 35, jank: 15 },
  webkit: { avg: 45, jank: 20 },
};

let failures = 0;
const check = (name, ok, detail) => {
  console.log(`${ok ? "PASS" : "FAIL"}: ${name}${detail ? ` -- ${detail}` : ""}`);
  if (!ok) failures++;
};

async function medianOf(engine, engineName) {
  const browser = await engine.launch();
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 }, deviceScaleFactor: 2 });
  await page.goto(`${base}${path}`, { waitUntil: "networkidle" });
  await page.waitForTimeout(1500);
  const runs = [];
  for (let i = 0; i < ATTEMPTS; i++) {
    const r = await measureScrollJank(page);
    console.log(`${engineName} attempt ${i + 1}:`, JSON.stringify(r));
    runs.push(r);
  }
  await browser.close();
  return [...runs].sort((a, b) => a.avg - b.avg)[Math.floor(runs.length / 2)];
}

console.log(`\n=== scroll-jank-gate @ ${base}${path} ===`);
for (const [engineName, engine] of Object.entries({ chromium, firefox, webkit })) {
  const budget = BUDGETS[engineName];
  const r = await medianOf(engine, engineName);
  check(`${engineName} avg <= ${budget.avg}ms`, r.avg <= budget.avg, `median ${r.avg}ms`);
  check(`${engineName} jank>33ms <= ${budget.jank}`, r["jank>33ms"] <= budget.jank, `median ${r["jank>33ms"]}`);
}

console.log(failures === 0 ? "ALL PASS" : `${failures} FAILURES`);
process.exit(failures === 0 ? 0 : 1);

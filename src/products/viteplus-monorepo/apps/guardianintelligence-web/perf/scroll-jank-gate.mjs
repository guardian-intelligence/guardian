// Runtime performance promotion gate for the letters treatment. Required
// check in the site-gate workflow: fails the build if the letters page's
// scroll-frame timing regresses past the thresholds below.
//
//   vp build && PORT=4179 HOST=127.0.0.1 node .output/server/index.mjs &
//   node perf/scroll-jank-gate.mjs
//
// Thresholds are calibrated against a REAL run of this exact job on the
// GitHub-hosted runner (2026-07-05), not just local measurements -- the
// runner turned out noisier than expected for Chromium/Firefox (shared
// 2-vCPU, no dedicated GPU) and WebKit specifically is not viable to gate
// on there at all: a real run measured avg 1957-3051ms/frame (vs ~40ms on
// real hardware) with as few as 2 sampled frames in a 5s window -- multiple
// orders of magnitude beyond anything seen locally, consistent with WebKit
// falling back to an unusable software path with no GPU on this runner.
// WebKit is therefore excluded from the CI gate; verify WebKit-specific
// changes with perf/scroll-jank.mjs / perf/transition-jank.mjs locally (on
// hardware with a GPU) or on a real device -- this is exactly why the
// letters-scroll-perf memory has Shovon testing on iOS separately. Chromium
// and Firefox budgets below have headroom over the real CI numbers observed
// (chromium: avg 20.5-21.6ms, jank 7-11; firefox: avg 40.4-43.6ms, jank 20).
// Three attempts per engine; the MEDIAN run is compared against budget --
// resistant to a single outlier sample in either direction, unlike a
// best-of-N (which would let one lucky sample from a genuinely regressed
// build slip under budget).
import { chromium, firefox } from "playwright";
import { measureScrollJank } from "./lib/scroll-measure.mjs";

const base = process.env.BASE ?? "http://127.0.0.1:4179";
const path = process.env.PAGE ?? "/letters/dear-shovon";
const ATTEMPTS = 3;

const BUDGETS = {
  chromium: { avg: 30, jank: 15 },
  firefox: { avg: 55, jank: 25 },
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
for (const [engineName, engine] of Object.entries({ chromium, firefox })) {
  const budget = BUDGETS[engineName];
  const r = await medianOf(engine, engineName);
  check(`${engineName} avg <= ${budget.avg}ms`, r.avg <= budget.avg, `median ${r.avg}ms`);
  check(`${engineName} jank>33ms <= ${budget.jank}`, r["jank>33ms"] <= budget.jank, `median ${r["jank>33ms"]}`);
}

console.log(failures === 0 ? "ALL PASS" : `${failures} FAILURES`);
process.exit(failures === 0 ? 0 : 1);

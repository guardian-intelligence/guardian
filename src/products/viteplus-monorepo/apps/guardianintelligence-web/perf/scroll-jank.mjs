// Scroll-jank ablation harness for the letters treatment.
//
// Drives a 5s triangle scroll over a rendered page while sampling rAF frame
// deltas and longtasks, once per ablation (each ablation injects CSS that
// disables ONE suspected cost). Diagnose in the engine that hurts: Firefox
// showed 58ms/frame avg where Chromium on a fast dev box holds a flat 60fps.
//
//   npx playwright install firefox            # once
//   vp build && PORT=4179 HOST=127.0.0.1 node .output/server/index.mjs &
//   node perf/scroll-jank.mjs firefox         # or: chromium | webkit
//   BASE=https://pr-123.guardianintelligence.org node perf/scroll-jank.mjs firefox
//
// Reading the table: p50 at ~17ms is vsync; a healthy page keeps avg==p50.
// The gap between "baseline" and an ablation row is that feature's real
// scroll cost. 2026-07-03 findings this harness produced: the stacked
// clip-path grid fade was the whole letter-page jank (58ms -> 18ms); the
// per-paragraph SVG hand filter was free; layerization hints did nothing.
import { chromium, firefox, webkit } from "playwright";
import { measureScrollJank } from "./lib/scroll-measure.mjs";

const base = process.env.BASE ?? "http://127.0.0.1:4179";
const path = process.env.PAGE ?? "/letters/dear-shovon";
const engines = { chromium, firefox, webkit };
const engineName = process.argv[2] ?? "firefox";
const engine = engines[engineName];
if (!engine) throw new Error(`unknown engine ${engineName}`);

const ABLATIONS = [
  { name: "baseline", css: "" },
  {
    name: "no-hand-filter",
    css: `[data-treatment="letters"] [data-letter-body] p,
          [data-treatment="letters"] [data-letter-body] li,
          [data-treatment="letters"] [data-letter-body] blockquote,
          [data-treatment="letters"] [data-letter-body] h2,
          [data-treatment="letters"] [data-letter-body] h3,
          [data-treatment="letters"] p[data-letter-slot="body"],
          [data-treatment="letters"] [data-letter-slot="salutation"] { filter: none !important; }`,
  },
  {
    name: "no-bloom",
    css: `[data-treatment="letters"] [data-letter-body],
          [data-treatment="letters"] [data-letter-slot="body"],
          [data-treatment="letters"] [data-letter-slot="salutation"] { text-shadow: none !important; }`,
  },
  {
    name: "no-tilt-flow",
    css: `[class*="letter-tilt-"] { transform: none !important; display: inline !important; }
          [class*="letter-flow-"] { position: static !important; }`,
  },
  { name: "no-grid-zones", css: ".letters-paper-zone { display: none !important; }" },
  { name: "no-tooth", css: ".letters-paper-tooth { display: none !important; }" },
  { name: "no-wash", css: ".letters-paper-wash { display: none !important; }" },
  {
    name: "no-paper-layers",
    css: `[data-treatment="letters"] .pointer-events-none.absolute.inset-0 { display: none !important; }`,
  },
];

const browser = await engine.launch();
const page = await browser.newPage({
  viewport: { width: 1280, height: 900 },
  deviceScaleFactor: 2,
});
await page.goto(`${base}${path}`, { waitUntil: "networkidle" });
await page.waitForTimeout(1500);

console.log(`\n=== ${engineName} @2x 1280x900, 5s triangle scroll, ${base}${path} ===`);
for (const a of ABLATIONS) {
  let handle = null;
  if (a.css) handle = await page.addStyleTag({ content: a.css });
  await page.waitForTimeout(400);
  const r = await measureScrollJank(page);
  console.log(a.name.padEnd(16), JSON.stringify(r));
  if (handle) await handle.evaluate((el) => el.remove());
}
await browser.close();

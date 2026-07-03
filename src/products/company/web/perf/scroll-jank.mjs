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

async function measure(page) {
  return page.evaluate(async () => {
    scrollTo(0, 0);
    await new Promise((r) => setTimeout(r, 300));
    const deltas = [];
    const longtasks = [];
    try {
      new PerformanceObserver((l) =>
        l.getEntries().forEach((e) => longtasks.push(Math.round(e.duration))),
      ).observe({ type: "longtask" });
    } catch {
      // longtask observer is Chromium-only; frame deltas still tell the story
    }
    let last = performance.now();
    let running = true;
    const tick = (t) => {
      deltas.push(t - last);
      last = t;
      if (running) requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
    const H = document.documentElement.scrollHeight - innerHeight;
    const start = performance.now();
    const dur = 5000;
    await new Promise((res) => {
      const step = () => {
        const p = (performance.now() - start) / dur;
        if (p >= 1) {
          running = false;
          return res();
        }
        const tri = p < 0.5 ? p * 2 : 2 - p * 2;
        scrollTo(0, Math.round(H * tri));
        requestAnimationFrame(step);
      };
      step();
    });
    deltas.shift();
    deltas.sort((a, b) => a - b);
    const q = (f) => deltas[Math.floor(f * (deltas.length - 1))];
    return {
      frames: deltas.length,
      avg: +(deltas.reduce((s, d) => s + d, 0) / deltas.length).toFixed(1),
      p50: +q(0.5).toFixed(1),
      p95: +q(0.95).toFixed(1),
      max: +q(1).toFixed(1),
      "jank>33ms": deltas.filter((d) => d > 33.4).length,
      longtasks: longtasks.length,
    };
  });
}

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
  const r = await measure(page);
  console.log(a.name.padEnd(16), JSON.stringify(r));
  if (handle) await handle.evaluate((el) => el.remove());
}
await browser.close();

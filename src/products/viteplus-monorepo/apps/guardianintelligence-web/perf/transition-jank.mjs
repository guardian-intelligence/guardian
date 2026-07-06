// Page-transition jank harness: /letters -> /letters/$slug via the top
// index entry's morph (the only entry with viewTransition + morph=true).
// Frame timing over the transition window (move 900ms + root 240ms, plus
// slack for the continuation-unfurl that starts right after), triggered by
// a real click, not a synthetic scroll.
//
//   vp build && PORT=4179 HOST=127.0.0.1 node .output/server/index.mjs &
//   node perf/transition-jank.mjs firefox         # or: chromium | webkit
//
// 2026-07-05: this harness is what showed --letter-spring-move's overshoot
// was NOT the transition's real cost (an ablation swapping it for a
// monotonic ease barely moved frame timing on any engine) -- the curve was
// changed to a plain cubic-bezier for the DESIGN reason (unfurl, not
// vibrate), not a perf one, and there is no longer an overshoot to ablate.
// Remaining ablations isolate what IS left: the continuation reveal, and the
// view-transition wrapper itself (root crossfade + old/new) vs. the raw cost
// of mounting a fresh letter page's worth of SVG-filtered paragraphs.
import { chromium, firefox, webkit } from "playwright";
import { startFrameSample, readFrameSample } from "./lib/frame-sample.mjs";

const base = process.env.BASE ?? "http://127.0.0.1:4179";
const engines = { chromium, firefox, webkit };
const engineName = process.argv[2] ?? "firefox";
const engine = engines[engineName];
if (!engine) throw new Error(`unknown engine ${engineName}`);

const WINDOW_MS = 1600; // 900 move + 240 root + 500 continuation, plus slack

const ABLATIONS = [
  { name: "baseline", css: "" },
  {
    name: "no-continuation-unfurl",
    css: `[data-letter-continuation][data-letter-continuation-animate] { animation: none !important; }`,
  },
  {
    name: "no-view-transition",
    css: `:root::view-transition-group(*),
          :root::view-transition-old(*),
          :root::view-transition-new(*) { animation: none !important; }`,
  },
];

async function measureOpen(page) {
  await page.waitForTimeout(500);
  await startFrameSample(page);
  await page.locator("[data-letter-entry]").first().click();
  await page.waitForTimeout(WINDOW_MS);
  return readFrameSample(page);
}

const browser = await engine.launch();
const page = await browser.newPage({
  viewport: { width: 1280, height: 900 },
  deviceScaleFactor: 2,
});

console.log(`\n=== ${engineName} @2x 1280x900, letter-open transition, ${base}/letters ===`);
for (const a of ABLATIONS) {
  // Each iteration navigates back to /letters fresh (via measureOpen's own
  // goto), which drops any style tag from the previous iteration's
  // now-different-page DOM -- no explicit cleanup needed or safe to do
  // (the page has already navigated to $slug by the time we'd remove it).
  await page.goto(`${base}/letters`, { waitUntil: "networkidle" });
  if (a.css) await page.addStyleTag({ content: a.css });
  const r = await measureOpen(page);
  console.log(a.name.padEnd(20), JSON.stringify(r));
}
await browser.close();

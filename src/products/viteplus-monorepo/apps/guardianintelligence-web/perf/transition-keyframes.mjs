// Records the REAL browser Animation objects driving the letter-open
// transition (via document.getAnimations()), not an inference from CSS
// source -- confirms actual duration/easing/currentTime progression per
// pseudo-element. Useful for a quick timing check, but NOT sufficient on
// its own: two view-transition-name'd elements can share identical timing
// and still look completely different (one dominated by a position/scale
// tween, the other by a bare opacity cross-fade) -- that gap is what made
// the salutation look like a delayed pop despite matching the date's
// duration exactly (2026-07-05, see app.css's letter-salutation-reveal
// comment). For the actual visual read, use transition-video.mjs +
// ffmpeg frame extraction; page.screenshot() does NOT reliably capture
// view-transition pseudo-element state mid-flight.
//
//   vp build && PORT=4179 HOST=127.0.0.1 node .output/server/index.mjs &
//   node perf/transition-keyframes.mjs chromium
import { chromium, firefox, webkit } from "playwright";

const base = process.env.BASE ?? "http://127.0.0.1:4179";
const engines = { chromium, firefox, webkit };
const engineName = process.argv[2] ?? "chromium";
const engine = engines[engineName];
if (!engine) throw new Error(`unknown engine ${engineName}`);

const browser = await engine.launch();
const page = await browser.newPage({ viewport: { width: 1280, height: 900 }, deviceScaleFactor: 2 });
await page.goto(`${base}/letters`, { waitUntil: "networkidle" });
await page.waitForTimeout(300);

await page.locator("[data-letter-entry]").first().click();
await page.waitForTimeout(150); // let the transition's pseudo-element tree + animations spin up

const animations = await page.evaluate(() => {
  return document
    .getAnimations({ subtree: true })
    .map((a) => {
      const timing = a.effect?.getTiming?.() ?? {};
      return {
        pseudoElement: a.effect?.pseudoElement ?? null,
        duration: timing.duration,
        easing: timing.easing,
        playState: a.playState,
        currentTime: typeof a.currentTime === "number" ? Math.round(a.currentTime) : a.currentTime,
      };
    })
    .filter((a) => a.pseudoElement);
});

console.log(`\n=== ${engineName} @ ${base}/letters -- live view-transition animations at t+150ms ===`);
for (const a of animations) console.log(JSON.stringify(a));

await page.waitForTimeout(1200);
await browser.close();

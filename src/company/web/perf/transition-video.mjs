// Records real video of the letter-open transition (screenshots settle the
// view-transition compositor state instead of capturing it mid-flight;
// video does not).
//
//   vp build && PORT=4179 HOST=127.0.0.1 node .output/server/index.mjs &
//   node perf/transition-video.mjs open /tmp/out   (or: return)
import { chromium } from "playwright";

const base = process.env.BASE ?? "http://127.0.0.1:4179";
const direction = process.argv[2] ?? "open";
const outDir = process.argv[3] ?? "/tmp/transition-video";

const browser = await chromium.launch();
const context = await browser.newContext({
  viewport: { width: 1280, height: 700 },
  deviceScaleFactor: 2,
  recordVideo: { dir: outDir, size: { width: 1280, height: 700 } },
});
const page = await context.newPage();

if (direction === "open") {
  await page.goto(`${base}/letters`, { waitUntil: "networkidle" });
  await page.waitForTimeout(300);
  await page.locator("[data-letter-entry]").first().click();
} else {
  await page.goto(`${base}/letters/dear-shovon`, { waitUntil: "networkidle" });
  await page.waitForTimeout(300);
  await page.locator("[data-letter-return]").click();
}
await page.waitForTimeout(1400);
await context.close();
await browser.close();
console.log("done");

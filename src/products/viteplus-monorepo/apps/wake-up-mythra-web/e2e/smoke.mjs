// The dev stack's own canary journey: one headless player signs in through
// the real code flow, connects, and proves the world is live. The shell
// wrapper (scripts/wum-dev.sh smoke) then asserts both telemetry lanes
// landed in the local ClickHouse; this script's only jobs are to be the
// player and to stay on the page until the beacon has flushed what the
// session emitted.
//
//   aspect mythra dev up
//   aspect mythra dev smoke
import { chromium } from "playwright";

const APP = process.env.WUM_APP_URL ?? "http://127.0.0.1:4254";
const PLAYER = process.env.WUM_SMOKE_PLAYER ?? "smoke-tester";

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// The journey's own deadline (portable, unlike GNU `timeout` in the shell
// wrapper); each step below is individually bounded well under it.
const deadline = setTimeout(() => {
  console.error("smoke: journey deadline exceeded");
  process.exit(124);
}, 110_000);

const browser = await chromium.launch({ args: ["--enable-features=WebTransport"] });
try {
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

  // Live means the world moves, not merely that a socket opened.
  const before = await page.evaluate(() => globalThis.__mythraDiag.tick);
  await sleep(2000);
  const after = await page.evaluate(() => globalThis.__mythraDiag.tick);
  if (!(after > before)) {
    throw new Error(`connected but the world is not advancing (tick ${before} -> ${after})`);
  }

  const spans = await page.evaluate(() => globalThis.__mythraProbe.drainSpans());
  const connected = spans.filter((s) => s.name === "wum.connected");
  if (connected.length === 0) {
    throw new Error("no wum.connected span reached the probe — the beacon has nothing to ship");
  }
  const dialMs = connected[connected.length - 1].attrs["wum.dial_ms"];
  const pageId = await page.evaluate(() => globalThis.__mythraPageId);
  if (!pageId) {
    throw new Error("no __mythraPageId on the probe page — cannot scope the events assertion");
  }

  // Wait for the emitSpan queue to empty: it only drains once the lazily
  // loaded beacon's flush runs (interval flush lands by ~20s after load,
  // idle ceiling included), which proves the beacon armed and this run's
  // events left the page over a normal fetch flush while it was alive.
  // Delivery to ClickHouse is the shell wrapper's assertion.
  await page.waitForFunction(() => (window.__guardianEvents?.length ?? 0) === 0, undefined, {
    timeout: 30000,
  });
  // Leave the way a real player does; the pagehide flush is now
  // belt-and-braces for stragglers, not the only ship path.
  await page.goto("about:blank");

  console.log(`SMOKE_JOURNEY dial_ms=${dialMs} page_id=${pageId} player=${PLAYER}`);
  clearTimeout(deadline);
} finally {
  await browser.close();
}

// Beacon lifecycle harness (manual, like scroll-jank.mjs):
//   vp build && PORT=4184 node .output/server/index.mjs &
//   node perf/beacon-e2e.mjs
// Verifies against a stubbed /api/events endpoint:
//   1. idle-loaded beacon publishes queued events on visibilitychange:hidden
//      via sendBeacon (Connect JSON shape, monotonic sessionSeq)
//   2. failed transport persists to localStorage (bounded)
//   3. replay on next init clears localStorage after a 2xx
import { chromium } from "playwright";

const base = process.env.BASE ?? "http://127.0.0.1:4184";
const RPC = "**/api/events/guardian.analytics.v1.EventService/Publish";
let failures = 0;
const check = (name, ok, detail = "") => {
  console.log(`${ok ? "PASS" : "FAIL"}: ${name}${detail ? ` — ${detail}` : ""}`);
  if (!ok) failures++;
};

const browser = await chromium.launch();
const ctx = await browser.newContext();
const page = await ctx.newPage();

// Phase 1: healthy endpoint. Collect published bodies.
const bodies = [];
await page.route(RPC, async (route) => {
  bodies.push(route.request().postData() ?? "");
  await route.fulfill({
    status: 200,
    contentType: "application/json",
    body: '{"accepted":1,"rejected":0}',
  });
});
await page.goto(base + "/", { waitUntil: "networkidle" });
await page.waitForTimeout(2500); // idle-load window
// Emit a synthetic event through the public interface, then hide the tab.
await page.evaluate(() => {
  window.__guardianEvents ??= [];
  window.__guardianEvents.push({
    name: "company.route_view",
    attrs: { "route.path": "/synthetic" },
    t: performance.now(),
  });
  Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
});
await page.waitForTimeout(1500);
check("hidden flush publishes", bodies.length >= 1, `${bodies.length} POSTs`);
if (bodies.length > 0) {
  const parsed = JSON.parse(bodies[bodies.length - 1]);
  check(
    "Connect JSON shape",
    typeof parsed.sentAtUnixMs === "string" && Array.isArray(parsed.events),
  );
  const seqs = parsed.events.map((e) => e.sessionSeq);
  check(
    "monotonic sessionSeq",
    seqs.every((s, i) => i === 0 || s > seqs[i - 1]),
    JSON.stringify(seqs),
  );
  const synthetic = parsed.events.find((e) => e.path === "/synthetic");
  check("route_view mapped", Boolean(synthetic) && synthetic.name === "company.route_view");
}

// Phase 2: sendBeacon rejection (the 64KiB-quota case) -> localStorage
// persistence. NOTE an aborted network request after sendBeacon queues is
// deliberately NOT recoverable — fire-and-forget is the accepted loss mode;
// the persist path triggers on sendBeacon returning false.
await page.unroute(RPC);
await page.route(RPC, (route) => route.abort());
await page.evaluate(() => {
  navigator.sendBeacon = () => false;
  window.__guardianEvents.push({ name: "click", attrs: { target: "cta" }, t: performance.now() });
  document.dispatchEvent(new Event("visibilitychange")); // still "hidden"
});
await page.waitForTimeout(1500);
const stored = await page.evaluate(() => localStorage.getItem("guardian_events_v1"));
check("failed send persists to LS", Boolean(stored) && JSON.parse(stored).length >= 1);

// Phase 3: healthy again -> replay on fresh page load clears LS.
await page.unroute(RPC);
const replayBodies = [];
await page.route(RPC, async (route) => {
  replayBodies.push(route.request().postData() ?? "");
  await route.fulfill({
    status: 200,
    contentType: "application/json",
    body: '{"accepted":1,"rejected":0}',
  });
});
await page.goto(base + "/", { waitUntil: "networkidle" });
await page.waitForTimeout(3500);
const afterReplay = await page.evaluate(() => localStorage.getItem("guardian_events_v1"));
check(
  "replay on load",
  replayBodies.some((b) => b.includes('"click"')),
);
check("LS cleared after replay", afterReplay === null || JSON.parse(afterReplay).length === 0);

await browser.close();
console.log(failures === 0 ? "ALL PASS" : `${failures} FAILURES`);
process.exit(failures === 0 ? 0 : 1);

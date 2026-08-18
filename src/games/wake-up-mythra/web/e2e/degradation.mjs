// The degradation drill: drives the netsim impairment proxy while a
// headless spectator plays through it, asserting the client's behavior
// under hostile conditions. Run against a proxied dev stack:
//
//   bazelisk run //src/chunkies/netsim &
//   WUM_DEV_PUBLIC_ADDR=127.0.0.1:14433 aspect mythra dev up
//   node e2e/degradation.mjs
//
// Legs:
//   clean     traffic relays, hash checks green
//   latency   +300ms/dir with jitter: measured rtt EWMA climbs
//   tunnel    8s blackhole: the replica free-runs on its local clock,
//             late events heal through the rollback ring — tiny deficit,
//             zero divergence (network death must NOT strand the sim)
//   cpu-stall 8s frozen main thread: the once-stuck-behind pathology.
//             The sim/clock module's FastForward must recover the whole
//             deficit within seconds (it once crawled at +0.5 ticks/s).
//   migrate   tower switch (upstream rebind): relay continues, no resync
import { chromium } from "playwright";

const APP = process.env.WUM_APP_URL ?? "http://127.0.0.1:4254";
const CTL = process.env.NETSIM_CONTROL ?? "http://127.0.0.1:14434";
const ctl = (path) =>
  fetch(`${CTL}${path}`, { method: path.startsWith("/state") ? "GET" : "POST" });

const browser = await chromium.launch({ args: ["--enable-features=WebTransport"] });
const page = await browser.newPage();
await page.goto(`${APP}/?spectate`, { waitUntil: "domcontentloaded" });
await page.waitForFunction(
  () => document.getElementById("status")?.textContent === "CONNECTED",
  undefined,
  { timeout: 30000 },
);
const probe = () =>
  page.evaluate(() => ({
    tick: globalThis.__mythraDiag.tick,
    rtt: document.getElementById("rtt")?.textContent,
    world: document.getElementById("world")?.textContent,
    checks: globalThis.__mythraDiag.checks,
    mismatches: globalThis.__mythraDiag.mismatches,
    resyncs: globalThis.__mythraDiag.resyncs,
  }));

await page.waitForTimeout(8000);
const clean = await probe();
console.log("clean relay:", JSON.stringify(clean));
console.log("proxy state:", await (await ctl("/state")).json());

await ctl("/impair?latency_ms=300&jitter_ms=40&seed=7");
await page.waitForTimeout(12000);
const laggy = await probe();
console.log("with +300ms/dir:", JSON.stringify(laggy));
await ctl("/impair?latency_ms=0&jitter_ms=0&seed=7");

const before = (await probe()).tick;
await ctl("/silence?ms=8000");
await page.waitForTimeout(11000);
const tunnel = await probe();
const tunnelDeficit = before + 11 * 24 - tunnel.tick;
console.log(`tunnel: deficit after heal=${tunnelDeficit} ticks, world=${tunnel.world}`);

await page.evaluate(() => {
  const end = performance.now() + 8000;
  while (performance.now() < end) {
    /* rAF frozen */
  }
});
// give FastForward 3s: the ~190-tick deficit must be fully recovered
await page.waitForTimeout(3000);
const afterStall = await probe();
const stallDeficit = tunnel.tick + (8 + 3) * 24 - afterStall.tick;
const recovered = Math.abs(stallDeficit) < 12 && afterStall.world === "✓";
console.log(
  `cpu-stall: deficit 3s after stall=${stallDeficit} ticks ${recovered ? "(fast recovery — clock module active)" : "(STUCK — clock regression)"}`,
);

await ctl("/migrate");
await page.waitForTimeout(6000);
const post = await probe();
console.log("post-migrate:", JSON.stringify(post));

const ok =
  clean.world === "✓" &&
  clean.mismatches === 0 &&
  parseInt(laggy.rtt) > 200 &&
  Math.abs(tunnelDeficit) < 48 &&
  tunnel.world === "✓" &&
  recovered &&
  post.checks > laggy.checks &&
  post.mismatches === 0;
console.log(ok ? "DEGRADATION DRILL OK" : "DEGRADATION DRILL FAILED");
await browser.close();
process.exit(ok ? 0 : 1);

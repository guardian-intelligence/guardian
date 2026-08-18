// One connected-client tick-rate drill for the local stack. The player
// exercises actions at 24Hz, asks the gateway's development-only control to
// journal a 48Hz rate_set, observes the event through the client core, and
// exercises the same actions again without navigating or reconnecting.
// The shell wrapper joins these client wire->apply facts to authority spans
// in local ClickHouse.
//
//   aspect mythra dev up
//   aspect mythra dev latency
import { chromium } from "playwright";

const APP = process.env.WUM_APP_URL ?? "http://127.0.0.1:4254";
const CONTROL = process.env.WUM_RATE_CONTROL_URL ?? "http://127.0.0.1:9634/dev/tick-rate";
const PARK = process.env.WUM_LATENCY_PARK ?? "park-mythra";
const PLAYER = process.env.WUM_LATENCY_PLAYER ?? `latency-${Date.now()}`;
const FROM_RATE = Number(process.env.WUM_LATENCY_FROM_HZ ?? 24);
const TO_RATE = Number(process.env.WUM_LATENCY_TO_HZ ?? 48);
const MOVE_SAMPLES = Number(process.env.WUM_LATENCY_MOVES ?? 40);
const BOOST_SAMPLES = Number(process.env.WUM_LATENCY_BOOSTS ?? 32);

if (BOOST_SAMPLES % 2 !== 0) throw new Error("WUM_LATENCY_BOOSTS must be even");

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const deadline = setTimeout(() => {
  console.error("latency: journey deadline exceeded");
  process.exit(124);
}, 120_000);

function quantile(values, q) {
  if (values.length === 0) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.min(sorted.length - 1, Math.ceil(q * sorted.length) - 1)];
}

const browser = await chromium.launch({ args: ["--enable-features=WebTransport"] });
try {
  const page = await browser.newPage();
  page.on("pageerror", (error) => console.error(`page error: ${error.message}`));
  await page.goto(`${APP}/?probe=1`, { waitUntil: "domcontentloaded" });
  await page.waitForFunction(
    () => document.getElementById("status")?.textContent === "CONNECTED",
    undefined,
    { timeout: 30_000 },
  );

  // Opening the page establishes the park authority as an anonymous
  // spectator. A previous drill deliberately leaves its durable rate at
  // TO_RATE, so restore the baseline before signing in and measuring the
  // player. The control returns only after the boundary is durable; the
  // player's following welcome therefore starts at FROM_RATE whether this
  // was a no-op on a fresh stack or a live reset on a used one.
  const baselineDeadline = Date.now() + 5_000;
  let baseline;
  while (Date.now() < baselineDeadline) {
    baseline = await fetch(
      `${CONTROL}?park=${encodeURIComponent(PARK)}&hz=${encodeURIComponent(FROM_RATE)}`,
      { method: "POST" },
    );
    if (baseline.ok) break;
    if (baseline.status !== 409) break;
    await sleep(25);
  }
  if (!baseline?.ok) {
    throw new Error(
      `baseline rate control returned ${baseline?.status}: ${await baseline?.text()}`,
    );
  }

  const [popup] = await Promise.all([page.waitForEvent("popup"), page.click("#signin")]);
  await popup.waitForSelector("input[name=u]", { timeout: 15_000 });
  await popup.fill("input[name=u]", PLAYER);
  await popup.click("button");
  await page.waitForFunction(
    () => document.getElementById("role")?.textContent?.startsWith("player"),
    undefined,
    { timeout: 30_000 },
  );

  const spans = [];
  const pull = async () => {
    spans.push(...(await page.evaluate(() => globalThis.__mythraProbe.drainSpans())));
  };
  const actions = (kind, rate) =>
    spans.filter(
      (span) =>
        span.name === "wum.action" &&
        span.attrs["wum.kind"] === kind &&
        Number(span.attrs["wum.rate_hz"]) === rate,
    );
  const waitForCount = async (kind, rate, count, timeoutMs = 20_000) => {
    const until = Date.now() + timeoutMs;
    while (Date.now() < until) {
      await pull();
      if (actions(kind, rate).length >= count) return;
      await sleep(2);
    }
    throw new Error(
      `${kind}@${rate}Hz: received ${actions(kind, rate).length}/${count} action facts`,
    );
  };

  await waitForCount("join", FROM_RATE, 1);
  await page.waitForSelector("#checkin:not([disabled])", { timeout: 15_000 });
  await page.click("#checkin");
  await waitForCount("check_in", FROM_RATE, 1);

  const canvas = await page.waitForSelector("#grid");
  const box = await canvas.boundingBox();
  if (!box) throw new Error("canvas has no bounding box");
  // These offsets intentionally avoid phase-locking to either scheduler.
  const delays = [7, 19, 31, 43, 13, 37, 53, 23, 47, 29];

  const exercise = async (rate) => {
    const moveStart = actions("move_to", rate).length;
    for (let i = 0; i < MOVE_SAMPLES; i++) {
      const x = box.x + box.width * (0.35 + 0.3 * ((i % 5) / 4));
      const y = box.y + box.height * (0.35 + 0.3 * (((i * 3) % 5) / 4));
      await page.mouse.click(x, y);
      await waitForCount("move_to", rate, moveStart + i + 1);
      await sleep(delays[i % delays.length]);
    }
    await waitForCount("move_to", rate, moveStart + MOVE_SAMPLES);

    const boostStart = actions("boost", rate).length;
    for (let i = 0; i < BOOST_SAMPLES; i++) {
      await page.dispatchEvent("#boost", i % 2 === 0 ? "pointerdown" : "pointerup");
      await waitForCount("boost", rate, boostStart + i + 1);
      await sleep(delays[((i + 1) * 7) % delays.length]);
    }
    await waitForCount("boost", rate, boostStart + BOOST_SAMPLES);
  };

  await exercise(FROM_RATE);
  await pull();

  const pageIdBefore = await page.evaluate(() => globalThis.__mythraPageId);
  const tickBefore = await page.evaluate(() => globalThis.__mythraDiag.tick);
  const resyncsBefore = await page.evaluate(() => globalThis.__mythraDiag.resyncs);
  const transitionAt = spans.length;

  const response = await fetch(
    `${CONTROL}?park=${encodeURIComponent(PARK)}&hz=${encodeURIComponent(TO_RATE)}`,
    { method: "POST" },
  );
  if (!response.ok) {
    throw new Error(`rate control returned ${response.status}: ${await response.text()}`);
  }

  const transitionDeadline = Date.now() + 15_000;
  let rateChange;
  while (Date.now() < transitionDeadline) {
    await pull();
    rateChange = spans
      .slice(transitionAt)
      .find(
        (span) =>
          span.name === "wum.tick_rate" &&
          Number(span.attrs["wum.from_hz"]) === FROM_RATE &&
          Number(span.attrs["wum.to_hz"]) === TO_RATE,
      );
    if (rateChange) break;
    await sleep(25);
  }
  if (!rateChange) throw new Error(`client never observed ${FROM_RATE}->${TO_RATE} rate_set`);

  await sleep(300);
  const tickAfter = await page.evaluate(() => globalThis.__mythraDiag.tick);
  if (!(tickAfter > tickBefore)) {
    throw new Error(`world stopped across rate boundary (tick ${tickBefore} -> ${tickAfter})`);
  }
  const pageIdAfter = await page.evaluate(() => globalThis.__mythraPageId);
  if (!pageIdBefore || pageIdAfter !== pageIdBefore) {
    throw new Error(
      `page identity changed across rate boundary (${pageIdBefore} -> ${pageIdAfter})`,
    );
  }

  await exercise(TO_RATE);
  await pull();

  const afterTransition = spans.slice(transitionAt);
  const forbidden = new Set([
    "wum.connected",
    "wum.redial",
    "wum.netcode_resync",
    "wum.netcode_teardown",
    "wum.netcode_restore",
  ]);
  const disruptions = afterTransition.filter((span) => forbidden.has(span.name));
  const resyncsAfter = await page.evaluate(() => globalThis.__mythraDiag.resyncs);
  if (disruptions.length !== 0 || resyncsAfter !== resyncsBefore) {
    throw new Error(
      `rate boundary disrupted the session: ${disruptions.map((span) => span.name).join(",")}`,
    );
  }

  const completed = spans.filter((span) => span.name === "wum.action");
  for (const rate of [FROM_RATE, TO_RATE]) {
    for (const kind of ["join", "check_in", "move_to", "boost"]) {
      const ms = actions(kind, rate).map((span) => Number(span.attrs["wum.ms"]));
      if (ms.length === 0) continue;
      console.log(
        `CLIENT_ACTION rate_hz=${rate} kind=${kind} n=${ms.length}` +
          ` p50_ms=${quantile(ms, 0.5)} p95_ms=${quantile(ms, 0.95)}` +
          ` min_ms=${Math.min(...ms)} max_ms=${Math.max(...ms)}`,
      );
    }
  }
  console.log(
    `RATE_CHANGE from_hz=${FROM_RATE} to_hz=${TO_RATE}` +
      ` tick=${rateChange.attrs["wum.tick"]} page_id=${pageIdAfter}` +
      ` world_tick=${tickBefore}->${tickAfter} redials=0 resyncs=0 restores=0`,
  );
  console.log(
    `LATENCY_JOURNEY rates=${FROM_RATE},${TO_RATE} actions=${completed.length} player=${PLAYER}`,
  );
  clearTimeout(deadline);
} finally {
  await browser.close();
}

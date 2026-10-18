// Encode regressions that only reproduce on real hardware codecs, so they
// run by hand against a live dev server — never in CI (headless Chrome
// encodes in software, where none of these failures exist). Each scenario
// drives the real editor in headed Chrome and judges it by the app's own
// telemetry events, so the production signals are exercised too.
//
//   vp dev                                  # in src/privatecut/web
//   vp run regression:encode                # all scenarios
//   ONLY=aac-stereo-floor vp run regression:encode
//
// Env: BASE (default http://127.0.0.1:4253), ONLY (comma list), FIXTURES
// (fixture cache dir), HEADLESS=1 (software codecs; most scenarios then
// prove nothing). Needs ffmpeg/ffprobe on PATH and Chrome installed.

import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { chromium } from "@playwright/test";

const base = process.env.BASE ?? "http://127.0.0.1:4253";
const only = process.env.ONLY?.split(",").filter(Boolean) ?? [];
const fixtureDir = process.env.FIXTURES ?? join(tmpdir(), "privatecut-regression");
const headless = process.env.HEADLESS === "1";
const LIMIT_BYTES = 4_000_000;

// 193 s portrait H.264 + stereo 44.1 kHz AAC, keyframe every 3 s — the shape
// of the 2026-10-17 production failures (720x1280 avc, 45 MB, 65 keyframes).
function portraitFixture() {
  const path = join(fixtureDir, "portrait-193s.mp4");
  if (existsSync(path)) return path;
  mkdirSync(fixtureDir, { recursive: true });
  const args = [
    "-hide_banner -loglevel error -y",
    "-f lavfi -i testsrc2=s=720x1280:r=30:d=193",
    "-f lavfi -i sine=f=440:d=193:sample_rate=44100",
    "-c:v libx264 -preset veryfast -b:v 1800k -g 90 -pix_fmt yuv420p",
    "-c:a aac -b:a 128k -ac 2 -shortest",
  ].flatMap((group) => group.split(" "));
  execFileSync("ffmpeg", [...args, path]);
  return path;
}

// Every emitSpan call lands in window.__guardianEvents; mirror pushes into a
// list the beacon's periodic drain cannot empty.
function captureEvents() {
  const seen = [];
  const queue = [];
  const push = queue.push.bind(queue);
  queue.push = (...items) => {
    seen.push(...items);
    return push(...items);
  };
  Object.defineProperty(window, "__regressionEvents", { value: seen });
  window.__guardianEvents = queue;
}

async function events(page, name) {
  const all = await page.evaluate(() => window.__regressionEvents);
  return all.filter((event) => event.name === name);
}

async function openEditor(page, fixture) {
  await page.goto(`${base}/`);
  // Hydrated: React has attached its props to the dropzone's input.
  await page.waitForFunction(() => {
    const input = document.querySelector('input[type="file"]');
    return input !== null && Object.keys(input).some((key) => key.startsWith("__reactProps"));
  });
  await page.setInputFiles('input[type="file"]', fixture);
  await page.locator("button", { hasText: "Create clip" }).waitFor({ timeout: 60_000 });
  // The preview has no intrinsic size until its metadata loads, and the
  // timeline moves when it gets one: measure nothing before then.
  await page.waitForFunction(() => (document.querySelector("video")?.videoWidth ?? 0) > 0);
}

// Drags a selection handle to `seconds` on the timeline and returns the
// selection readout ("0:00 – 0:57"). Throws when the handle did not land
// within two seconds of the target: every scenario's meaning depends on the
// selection length, so a missed drag must not pass as a run.
async function dragHandle(page, side, seconds, durationS) {
  const handle = page.getByRole("slider", {
    name: side === "start" ? "Selection start" : "Selection end",
  });
  const box = await handle.boundingBox();
  const strip = await page.locator(".select-none > div").first().boundingBox();
  const y = box.y + box.height / 2;
  await page.mouse.move(box.x + box.width / 2, y);
  await page.mouse.down();
  await page.mouse.move(strip.x + strip.width * (seconds / durationS), y, { steps: 12 });
  await page.mouse.up();
  const readout = (await page.locator(".select-none .text-mist-dim").first().textContent()) ?? "";
  const [startText = "", endText = ""] = readout.split("–").map((part) => part.trim());
  const landed = clockSeconds(side === "start" ? startText : endText);
  if (Math.abs(landed - seconds) > 2) {
    throw new Error(`${side} handle landed at ${landed}s, aimed at ${seconds}s (${readout})`);
  }
  return readout;
}

function clockSeconds(text) {
  const [minutes = "0", seconds = "0"] = text.split(":");
  return Number(minutes) * 60 + Number(seconds);
}

// Resolves when the encode settles: the result card's download link, or the
// editor's failure line. Throws if neither appears in time.
async function settle(page, timeoutMs) {
  const handle = await page.waitForFunction(
    () => {
      const link = document.querySelector("a[download]");
      if (link !== null) return { done: link.getAttribute("href") };
      const failure = document.querySelector("main .text-red-200");
      return failure === null ? null : { failed: failure.textContent };
    },
    undefined,
    { timeout: timeoutMs, polling: 500 },
  );
  return handle.jsonValue();
}

async function waitForProgress(page) {
  await page.waitForFunction(
    () =>
      Array.from(document.querySelectorAll("main button")).some((button) =>
        /^(Encoding|Pass) .*[1-9]\d*%$/.test(button.textContent ?? ""),
      ),
    undefined,
    { timeout: 60_000, polling: 250 },
  );
}

// The finished clip's streams, read back with ffprobe.
async function probeOutput(page, href) {
  const base64 = await page.evaluate(async (url) => {
    const blob = await (await fetch(url)).blob();
    return new Promise((resolve) => {
      const reader = new FileReader();
      reader.onload = () =>
        resolve(typeof reader.result === "string" ? reader.result.split(",")[1] : "");
      reader.readAsDataURL(blob);
    });
  }, href);
  const bytes = Buffer.from(base64, "base64");
  const path = join(fixtureDir, "last-output.bin");
  writeFileSync(path, bytes);
  const probe = JSON.parse(
    execFileSync("ffprobe", ["-v", "error", "-show_streams", "-of", "json", path], {
      encoding: "utf8",
    }),
  );
  return { bytes: bytes.length, streams: probe.streams.map((stream) => stream.codec_type) };
}

// The beacon ships props as one JSON string and drops it whole past 2000
// characters (shared/ts/telemetry beacon.ts): an event that big arrives
// with its evidence missing. Mirrors toWire's exclusions.
async function oversizedEvents(page) {
  const all = await page.evaluate(() => window.__regressionEvents ?? []);
  return all
    .map((event) => {
      const { "route.path": _path, referrer: _referrer, ...rest } = event.attrs;
      return { name: event.name, length: JSON.stringify(rest).length };
    })
    .filter((event) => event.length > 2000);
}

function check(failures, condition, message) {
  if (!condition) failures.push(message);
}

const DURATION_S = 193.1;

const scenarios = {
  // 2026-10-17: trimming to ~40–75 s at 4 MB planned 64 kbps stereo AAC,
  // which crashes Chrome's GPU process on macOS; the encode then hung at 0%
  // with the window's compositing gone.
  async "aac-stereo-floor"(page, fixture, failures) {
    await openEditor(page, fixture);
    const range = await dragHandle(page, "end", 57, DURATION_S);
    await page.locator("button", { hasText: "Create clip" }).click();
    const outcome = await settle(page, 120_000);
    check(
      failures,
      outcome.done !== undefined,
      `encode did not finish: ${JSON.stringify(outcome)}`,
    );
    check(
      failures,
      (await events(page, "privatecut.gpu_context_lost")).length === 0,
      "GPU context was lost",
    );
    if (outcome.done !== undefined) {
      const output = await probeOutput(page, outcome.done);
      check(failures, output.bytes <= LIMIT_BYTES, `output ${output.bytes} B over the cap`);
      check(failures, output.streams.includes("audio"), `output lost its audio: ${output.streams}`);
      return `${range} → ${output.bytes} B, streams ${output.streams.join("+")}`;
    }
    return range;
  },

  // 2026-10-17: the full 180 s at 4 MB. The hardware encoder ignores
  // requests below its rate floor, so repricing grew the file every pass
  // (4.68 → 4.84 → 5.01 MB) until the pass limit, then failed.
  async "encoder-rate-floor"(page, fixture, failures) {
    await openEditor(page, fixture);
    await page.locator("button", { hasText: "Create clip" }).click();
    const outcome = await settle(page, 300_000);
    check(
      failures,
      outcome.done !== undefined,
      `encode did not finish: ${JSON.stringify(outcome)}`,
    );
    const [completed] = await events(page, "privatecut.encode_completed");
    if (outcome.done !== undefined) {
      const output = await probeOutput(page, outcome.done);
      check(failures, output.bytes <= LIMIT_BYTES, `output ${output.bytes} B over the cap`);
    }
    return completed === undefined ? "no completion event" : `trail ${completed.attrs.pass_trail}`;
  },

  // A codec that hangs without erroring must not hang the editor: freeze
  // the worker mid-encode, expect the stall to surface within the watchdog
  // window, then prove the recycled engine encodes again.
  async "stall-watchdog"(page, fixture, failures) {
    await openEditor(page, fixture);
    await page.locator("button", { hasText: "Create clip" }).click();
    await waitForProgress(page);
    const [worker] = page.workers();
    void worker
      ?.evaluate(() => {
        const until = Date.now() + 120_000;
        while (Date.now() < until) {
          // spin: the encode stops advancing
        }
      })
      .catch(() => {});
    const outcome = await settle(page, 60_000);
    check(
      failures,
      /stopped responding/.test(outcome.failed ?? ""),
      `no stall surfaced: ${JSON.stringify(outcome)}`,
    );
    const [failed] = await events(page, "privatecut.encode_failed");
    check(failures, failed?.attrs.kind === "stalled", `encode_failed kind ${failed?.attrs.kind}`);
    await dragHandle(page, "end", 57, DURATION_S);
    await page.locator("button", { hasText: "Create clip" }).click();
    const retry = await settle(page, 120_000);
    check(failures, retry.done !== undefined, `retry after stall failed: ${JSON.stringify(retry)}`);
    return `stall → ${failed?.attrs.message}; retry ${retry.done === undefined ? "failed" : "ok"}`;
  },

  // A renderer that dies mid-encode can't send its own events; the next
  // page load must report the encode as abandoned, joined by trace id.
  async "crash-breadcrumb"(page, fixture, failures, context) {
    await openEditor(page, fixture);
    await page.locator("button", { hasText: "Create clip" }).click();
    await waitForProgress(page);
    const [started] = await events(page, "privatecut.encode_started");
    // chrome://crash kills the tab's current renderer outright — no
    // pagehide, exactly like the production crash.
    await Promise.race([
      page.goto("chrome://crash").catch(() => {}),
      new Promise((resolve) => setTimeout(resolve, 5_000)),
    ]);
    const next = await context.newPage();
    await next.goto(`${base}/`);
    await next
      .waitForFunction(
        () =>
          window.__regressionEvents.some((event) => event.name === "privatecut.encode_abandoned"),
        undefined,
        { timeout: 45_000 },
      )
      .catch(() => {});
    const [abandoned] = await events(next, "privatecut.encode_abandoned");
    check(failures, abandoned !== undefined, "no encode_abandoned on the next load");
    check(failures, abandoned?.attrs.reason === "page_lost", `reason ${abandoned?.attrs.reason}`);
    check(
      failures,
      abandoned?.attrs["trace.id"] === started?.attrs["trace.id"],
      "abandoned event does not join the started encode",
    );
    await next.close();
    return `abandoned reason=${abandoned?.attrs.reason} pass=${abandoned?.attrs.pass}`;
  },

  // Any GPU-process death mid-encode (the 10-17 crash was one) must be
  // observed and must settle the encode — finished or failed — not hang.
  async "gpu-crash-mid-encode"(page, fixture, failures, context) {
    await openEditor(page, fixture);
    await page.locator("button", { hasText: "Create clip" }).click();
    await waitForProgress(page);
    // chrome://gpucrash is refused to automation; CDP can do it directly.
    const cdp = await context.browser().newBrowserCDPSession();
    await cdp.send("Browser.crashGpuProcess");
    const outcome = await settle(page, 120_000).catch(() => ({ hung: true }));
    check(failures, outcome.hung !== true, "encode hung after the GPU process died");
    const lost = await events(page, "privatecut.gpu_context_lost");
    check(
      failures,
      lost.some((event) => event.attrs.encoding === "true"),
      "GPU loss not reported",
    );
    const [failed] = await events(page, "privatecut.encode_failed");
    return outcome.done !== undefined
      ? "survived: finished"
      : `settled: ${failed?.attrs.kind}/${failed?.attrs["error.type"]}`;
  },
};

const fixture = portraitFixture();
const selected = Object.keys(scenarios).filter((name) => only.length === 0 || only.includes(name));
const browser = await chromium.launch({ channel: "chrome", headless });
let failed = 0;
for (const name of selected) {
  // A fresh context per scenario: crashes and GPU loss must not leak.
  const context = await browser.newContext({ viewport: { width: 1280, height: 1000 } });
  await context.addInitScript(captureEvents);
  const page = await context.newPage();
  const failures = [];
  let detail = "";
  const startedAt = Date.now();
  try {
    detail = await scenarios[name](page, fixture, failures, context);
  } catch (error) {
    failures.push(error instanceof Error ? error.message.split("\n")[0] : String(error));
  }
  for (const open of context.pages()) {
    const oversized = await Promise.race([
      oversizedEvents(open).catch(() => []),
      new Promise((resolve) => setTimeout(() => resolve([]), 5_000)),
    ]);
    for (const event of oversized) {
      failures.push(`${event.name} props are ${event.length} chars; the beacon drops them`);
    }
  }
  await context.close().catch(() => {});
  const seconds = ((Date.now() - startedAt) / 1000).toFixed(1);
  if (failures.length > 0) failed += 1;
  process.stdout.write(
    `${failures.length === 0 ? "PASS" : "FAIL"} ${name} (${seconds}s) ${detail ?? ""}\n`,
  );
  for (const failure of failures) process.stdout.write(`     - ${failure}\n`);
}
await browser.close();
process.exit(failed === 0 ? 0 : 1);

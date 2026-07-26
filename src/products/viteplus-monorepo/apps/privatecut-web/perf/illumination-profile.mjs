import { mkdir, writeFile } from "node:fs/promises";
import { dirname } from "node:path";
import { chromium } from "@playwright/test";

const base = process.env.BASE ?? "http://127.0.0.1:4253";
const targetHz = Number(process.env.TARGET_HZ ?? "165");
const sampleMs = Number(process.env.SAMPLE_MS ?? "5000");
const attempts = Number(process.env.ATTEMPTS ?? "3");
const headless = process.env.HEADLESS !== "0";
const shouldGate = process.env.GATE === "1";
const outputPath = process.env.OUTPUT;

if (!Number.isFinite(targetHz) || targetHz <= 0) throw new Error("TARGET_HZ must be positive");
if (!Number.isInteger(sampleMs) || sampleMs < 1_000) throw new Error("SAMPLE_MS must be >= 1000");
if (!Number.isInteger(attempts) || attempts < 1) throw new Error("ATTEMPTS must be >= 1");

const targetFrameMs = 1_000 / targetHz;
const droppedFrameMs = targetFrameMs * 2;
const p95BudgetMs = targetFrameMs * 1.35;

function round(value, digits = 2) {
  return Number(value.toFixed(digits));
}

function median(values) {
  const sorted = [...values].sort((left, right) => left - right);
  return sorted[Math.floor(sorted.length / 2)];
}

function metricMap(payload) {
  return Object.fromEntries(payload.metrics.map(({ name, value }) => [name, value]));
}

async function installObservers(page) {
  await page.addInitScript(() => {
    window.__privatecutProfile = {
      cls: 0,
      events: [],
      lcp: null,
      longAnimationFrames: [],
      longtasks: [],
    };

    const observe = (type, callback) => {
      try {
        const options = { type, buffered: true };
        if (type === "event") options.durationThreshold = 16;
        new PerformanceObserver((list) => callback(list.getEntries())).observe(options);
      } catch {}
    };

    observe("largest-contentful-paint", (entries) => {
      const last = entries.at(-1);
      if (last) window.__privatecutProfile.lcp = last.startTime;
    });
    observe("layout-shift", (entries) => {
      for (const entry of entries) {
        if (!entry.hadRecentInput) window.__privatecutProfile.cls += entry.value;
      }
    });
    observe("longtask", (entries) => {
      for (const entry of entries) {
        window.__privatecutProfile.longtasks.push({
          duration: entry.duration,
          startTime: entry.startTime,
        });
      }
    });
    observe("long-animation-frame", (entries) => {
      for (const entry of entries) {
        window.__privatecutProfile.longAnimationFrames.push({
          blockingDuration: entry.blockingDuration,
          duration: entry.duration,
          scripts: [...entry.scripts].map((script) => ({
            duration: script.duration,
            forcedStyleAndLayoutDuration: script.forcedStyleAndLayoutDuration,
            functionName: script.sourceFunctionName,
            invoker: script.invoker,
            sourceUrl: script.sourceURL,
          })),
        });
      }
    });
    observe("event", (entries) => {
      for (const entry of entries) {
        if (entry.interactionId > 0) {
          window.__privatecutProfile.events.push({
            duration: entry.duration,
            interactionId: entry.interactionId,
            name: entry.name,
          });
        }
      }
    });
  });
}

async function sampleFrames(page, mode) {
  return page.evaluate(
    async ({ durationMs, movePointer, slowFrameMs }) => {
      const deltas = [];
      const longtasks = [];
      let active = true;
      let previous = performance.now();
      const observer = new PerformanceObserver((list) => {
        for (const entry of list.getEntries()) longtasks.push(entry.duration);
      });
      try {
        observer.observe({ type: "longtask" });
      } catch {}

      const tick = (timestamp) => {
        deltas.push(timestamp - previous);
        previous = timestamp;
        if (movePointer) {
          const phase = ((timestamp % durationMs) / durationMs) * Math.PI * 2;
          const x = innerWidth * (0.5 + Math.sin(phase) * 0.42);
          const y = innerHeight * (0.48 + Math.sin(phase * 2) * 0.32);
          const target = document.elementFromPoint(x, y) ?? document;
          target.dispatchEvent(
            new PointerEvent("pointermove", {
              bubbles: true,
              clientX: x,
              clientY: y,
              pointerId: 1,
              pointerType: "mouse",
            }),
          );
        }
        if (active) requestAnimationFrame(tick);
      };

      requestAnimationFrame(tick);
      await new Promise((resolve) => setTimeout(resolve, durationMs));
      active = false;
      observer.disconnect();
      deltas.shift();

      const sorted = [...deltas].sort((left, right) => left - right);
      const total = deltas.reduce((sum, value) => sum + value, 0);
      return {
        averageMs: total / Math.max(deltas.length, 1),
        dropped: deltas.filter((value) => value > slowFrameMs).length,
        droppedRatio:
          deltas.filter((value) => value > slowFrameMs).length / Math.max(deltas.length, 1),
        frames: deltas.length,
        maxMs: sorted.at(-1) ?? 0,
        p50Ms: sorted[Math.floor(sorted.length * 0.5)] ?? 0,
        p95Ms: sorted[Math.floor(sorted.length * 0.95)] ?? 0,
        p99Ms: sorted[Math.floor(sorted.length * 0.99)] ?? 0,
        sampleHz: 1_000 / (sorted[Math.floor(sorted.length * 0.5)] ?? 1_000),
        longtasks: longtasks.length,
        totalLongtaskMs: longtasks.reduce((sum, value) => sum + value, 0),
      };
    },
    { durationMs: sampleMs, movePointer: mode === "pointer", slowFrameMs: droppedFrameMs },
  );
}

function summarizeRuns(runs) {
  return Object.fromEntries(
    Object.keys(runs[0]).map((key) => [key, round(median(runs.map((run) => run[key])))]),
  );
}

const browser = await chromium.launch({ channel: "chrome", headless });
const context = await browser.newContext({
  deviceScaleFactor: 2,
  reducedMotion: "no-preference",
  viewport: { width: 1_440, height: 900 },
});
const page = await context.newPage();
await installObservers(page);
await page.bringToFront();
const cdp = await context.newCDPSession(page);
await cdp.send("Performance.enable");

const beforeNavigation = metricMap(await cdp.send("Performance.getMetrics"));
await page.goto(base, { waitUntil: "networkidle" });
await page.waitForTimeout(4_000);
const afterNavigation = metricMap(await cdp.send("Performance.getMetrics"));

const idleRuns = [];
const pointerRuns = [];
for (let attempt = 0; attempt < attempts; attempt += 1) {
  idleRuns.push(await sampleFrames(page, "idle"));
  pointerRuns.push(await sampleFrames(page, "pointer"));
}

const settledFrameStart = await page
  .locator(".illumination-scene")
  .evaluate((element) => Number(element.dataset.frameCount ?? "0"))
  .catch(() => null);
await page.waitForTimeout(1_000);
const settledFrameEnd = await page
  .locator(".illumination-scene")
  .evaluate((element) => Number(element.dataset.frameCount ?? "0"))
  .catch(() => null);

const field = page.locator(".link-input__field");
await field.click();
await field.fill("https://x.com/privatecut-performance-probe");
await page.waitForTimeout(500);

const pageMetrics = await page.evaluate(() => {
  const navigation = performance.getEntriesByType("navigation")[0];
  const paints = Object.fromEntries(
    performance.getEntriesByType("paint").map((entry) => [entry.name, entry.startTime]),
  );
  const resources = performance.getEntriesByType("resource");
  const profile = window.__privatecutProfile;
  const interactionGroups = new Map();
  for (const event of profile.events) {
    const current = interactionGroups.get(event.interactionId) ?? 0;
    interactionGroups.set(event.interactionId, Math.max(current, event.duration));
  }
  const interactionDurations = [...interactionGroups.values()];
  const illumination = document.querySelector(".illumination-scene");
  return {
    animations: {
      infinite: document
        .getAnimations()
        .filter((animation) => animation.effect?.getTiming().iterations === Number.POSITIVE_INFINITY)
        .length,
      total: document.getAnimations().length,
    },
    canvases: [...document.querySelectorAll("canvas")].map((canvas) => ({
      backingHeight: canvas.height,
      backingWidth: canvas.width,
      className: canvas.className,
      cssHeight: canvas.clientHeight,
      cssWidth: canvas.clientWidth,
    })),
    cls: profile.cls,
    domElements: document.querySelectorAll("*").length,
    fcp: paints["first-contentful-paint"] ?? null,
    illumination: illumination
      ? {
          frameCount: Number(illumination.dataset.frameCount ?? "0"),
          mode: illumination.dataset.mode ?? "unknown",
          state: illumination.dataset.state ?? "unknown",
        }
      : null,
    inp: interactionDurations.length > 0 ? Math.max(...interactionDurations) : null,
    lcp: profile.lcp,
    longAnimationFrames: {
      count: profile.longAnimationFrames.length,
      maxBlockingDuration:
        profile.longAnimationFrames.length > 0
          ? Math.max(...profile.longAnimationFrames.map((entry) => entry.blockingDuration))
          : 0,
      maxDuration:
        profile.longAnimationFrames.length > 0
          ? Math.max(...profile.longAnimationFrames.map((entry) => entry.duration))
          : 0,
      slowest: [...profile.longAnimationFrames]
        .sort((left, right) => right.duration - left.duration)
        .slice(0, 3),
    },
    slowestInteractions: [...profile.events]
      .sort((left, right) => right.duration - left.duration)
      .slice(0, 5),
    navigation: navigation
      ? {
          domContentLoaded: navigation.domContentLoadedEventEnd,
          load: navigation.loadEventEnd,
          responseEnd: navigation.responseEnd,
          ttfb: navigation.responseStart - navigation.requestStart,
        }
      : null,
    resources: {
      count: resources.length,
      decodedBodySize: resources.reduce((sum, entry) => sum + (entry.decodedBodySize || 0), 0),
      transferSize: resources.reduce((sum, entry) => sum + (entry.transferSize || 0), 0),
    },
    totalBlockingTime: profile.longtasks
      .filter((entry) => entry.startTime >= (paints["first-contentful-paint"] ?? 0))
      .reduce((sum, entry) => sum + Math.max(0, entry.duration - 50), 0),
  };
});

const afterProfile = metricMap(await cdp.send("Performance.getMetrics"));
const report = {
  axes: {
    cdp: {
      heapMb: round((afterProfile.JSHeapUsedSize ?? 0) / 1024 / 1024),
      layoutMs: round(
        ((afterProfile.LayoutDuration ?? 0) - (beforeNavigation.LayoutDuration ?? 0)) * 1_000,
      ),
      scriptMs: round(
        ((afterProfile.ScriptDuration ?? 0) - (beforeNavigation.ScriptDuration ?? 0)) * 1_000,
      ),
      styleMs: round(
        ((afterProfile.RecalcStyleDuration ?? 0) -
          (beforeNavigation.RecalcStyleDuration ?? 0)) *
          1_000,
      ),
      taskMs: round(
        ((afterProfile.TaskDuration ?? 0) - (beforeNavigation.TaskDuration ?? 0)) * 1_000,
      ),
    },
    idle: summarizeRuns(idleRuns),
    page: pageMetrics,
    pointer: summarizeRuns(pointerRuns),
    settledFrameDelta:
      settledFrameStart === null || settledFrameEnd === null
        ? null
        : settledFrameEnd - settledFrameStart,
    startupCdp: {
      taskMs: round(
        ((afterNavigation.TaskDuration ?? 0) - (beforeNavigation.TaskDuration ?? 0)) * 1_000,
      ),
    },
  },
  budgets: {
    cls: 0.1,
    inpMs: 200,
    lcpMs: 2_500,
    p95FrameMs: round(p95BudgetMs),
    slowFrameMs: round(droppedFrameMs),
    targetFrameMs: round(targetFrameMs),
    targetHz,
    totalBlockingTimeMs: 200,
  },
  environment: {
    attempts,
    base,
    capturedAt: new Date().toISOString(),
    headless,
    sampleMs,
    userAgent: await page.evaluate(() => navigator.userAgent),
  },
};

await context.close();
await browser.close();

const failures = [];
if (shouldGate) {
  const { page: measuredPage, pointer, settledFrameDelta } = report.axes;
  if (pointer.p95Ms > p95BudgetMs) {
    failures.push(`pointer p95 ${pointer.p95Ms}ms > ${round(p95BudgetMs)}ms`);
  }
  if (pointer.droppedRatio > 0.01) {
    failures.push(`pointer dropped-frame ratio ${pointer.droppedRatio} > 0.01`);
  }
  if (measuredPage.lcp !== null && measuredPage.lcp > 2_500) {
    failures.push(`LCP ${round(measuredPage.lcp)}ms > 2500ms`);
  }
  if (measuredPage.cls > 0.1) failures.push(`CLS ${measuredPage.cls} > 0.1`);
  if (measuredPage.inp !== null && measuredPage.inp > 200) {
    failures.push(`INP ${round(measuredPage.inp)}ms > 200ms`);
  }
  if (measuredPage.totalBlockingTime > 200) {
    failures.push(`TBT ${round(measuredPage.totalBlockingTime)}ms > 200ms`);
  }
  if (measuredPage.illumination && measuredPage.illumination.state !== "idle") {
    failures.push(`illumination did not settle: ${measuredPage.illumination.state}`);
  }
  if (settledFrameDelta !== null && settledFrameDelta !== 0) {
    failures.push(`illumination rendered ${settledFrameDelta} idle frames`);
  }
}

process.stdout.write(`${JSON.stringify({ ...report, failures }, null, 2)}\n`);
if (outputPath) {
  await mkdir(dirname(outputPath), { recursive: true });
  await writeFile(outputPath, `${JSON.stringify({ ...report, failures }, null, 2)}\n`);
}
process.exitCode = failures.length === 0 ? 0 : 1;

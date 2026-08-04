import assert from "node:assert/strict";
import fs from "node:fs/promises";
import { chromium } from "@playwright/test";

const base = process.env.BASE ?? "http://127.0.0.1:4252";
const macOsChrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
let executablePath = process.env.BROWSER_EXECUTABLE;

if (!executablePath && process.platform === "darwin") {
  try {
    await fs.access(macOsChrome);
    executablePath = macOsChrome;
  } catch {
    // Use Playwright's pinned browser outside a developer Mac.
  }
}

const browser = await chromium.launch(executablePath ? { executablePath } : {});
const page = await browser.newPage({ viewport: { height: 900, width: 1_440 } });

try {
  await page.goto(`${base}/`, { waitUntil: "networkidle" });
  await page.waitForFunction(
    () =>
      document.documentElement.dataset.companyExperience === "static" ||
      document.querySelector("[data-title-materialization]")?.dataset.materializeProgress ===
        "1.0000",
  );

  const host = await page.$(".persistent-company-home");
  const canvas = await page.$(".illumination-canvas");
  assert(host, "expected the persistent company-home host");
  assert(canvas, "expected the illumination canvas");
  const experience = await page.locator("html").getAttribute("data-company-experience");

  await page.getByRole("link", { exact: true, name: "Letters" }).click();
  await page.waitForURL(`${base}/letters`);
  const suspended = await canvas.evaluate((element) => ({
    frames: Number(element.dataset.frameCount ?? 0),
    routeState: element.dataset.routeState,
  }));
  await page.waitForTimeout(300);
  const suspendedAfter = Number(await canvas.getAttribute("data-frame-count"));
  const hiddenState = await host.evaluate((element) => ({
    active: element.dataset.companyHomeActive,
    inert: element.hasAttribute("inert"),
    visibility: getComputedStyle(element).visibility,
  }));

  assert.equal(suspended.routeState, "suspended");
  assert.equal(suspendedAfter, suspended.frames);
  assert.deepEqual(hiddenState, { active: "false", inert: true, visibility: "hidden" });

  await page.getByRole("link", { exact: true, name: "Home" }).click();
  await page.waitForURL(`${base}/`);
  const returnFrames = await page.evaluate(
    () =>
      new Promise((resolve) => {
        const samples = [];
        const sample = () => {
          const hostElement = document.querySelector(".persistent-company-home");
          const canvasElement = document.querySelector(".illumination-canvas");
          samples.push({
            active: hostElement?.getAttribute("data-company-home-active"),
            canvasHeight: canvasElement?.getBoundingClientRect().height ?? 0,
            canvasMode: canvasElement?.getAttribute("data-mode"),
            experience: document.documentElement.dataset.companyExperience,
            hostHeight: hostElement?.getBoundingClientRect().height ?? 0,
            visibility: hostElement ? getComputedStyle(hostElement).visibility : "missing",
          });
          if (samples.length < 8) requestAnimationFrame(sample);
          else resolve(samples);
        };
        requestAnimationFrame(sample);
      }),
  );
  const resumed = Number(await canvas.getAttribute("data-frame-count"));

  assert(
    await host.evaluate(
      (element) => element === document.querySelector(".persistent-company-home"),
    ),
  );
  assert(
    await canvas.evaluate((element) => element === document.querySelector(".illumination-canvas")),
  );
  assert(resumed >= suspendedAfter);
  assert(
    returnFrames.every(
      (frame) =>
        frame.active === "true" &&
        frame.hostHeight > 0 &&
        frame.experience === experience &&
        (frame.experience === "static" || (frame.canvasHeight > 0 && frame.canvasMode !== "css")) &&
        frame.visibility === "visible",
    ),
  );

  console.log(
    JSON.stringify({
      offRouteFrameDelta: suspendedAfter - suspended.frames,
      experience,
      retainedCanvas: true,
      retainedHost: true,
      returnFrames: returnFrames.length,
      resumedFrameDelta: resumed - suspendedAfter,
    }),
  );
} finally {
  await browser.close();
}

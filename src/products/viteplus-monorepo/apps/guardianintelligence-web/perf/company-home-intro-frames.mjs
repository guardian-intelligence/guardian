import fs from "node:fs/promises";
import path from "node:path";
import { chromium } from "@playwright/test";

const base = process.env.BASE ?? "http://127.0.0.1:4252";
const targetUrl = process.env.TARGET_URL ?? `${base}/`;
const titleSelector = process.env.TITLE_SELECTOR ?? ".company-home-title__base";
const captureMs = Number.parseInt(process.env.CAPTURE_MS ?? "4750", 10);
const profileName = process.argv[2] ?? "mobile";
const outputRoot = process.argv[3] ?? "/tmp/company-home-intro";
const profiles = {
  desktop: { width: 1440, height: 900 },
  mobile: { width: 390, height: 844 },
};
const viewport = profiles[profileName];
const macOsChrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";

if (!viewport) throw new Error(`unknown profile ${profileName}`);

const outputDir = path.join(outputRoot, profileName);
await fs.rm(outputDir, { force: true, recursive: true });
await fs.mkdir(outputDir, { recursive: true });

let executablePath = process.env.BROWSER_EXECUTABLE;
if (!executablePath && process.platform === "darwin") {
  try {
    await fs.access(macOsChrome);
    executablePath = macOsChrome;
  } catch {
    // Fall through to Playwright's pinned browser outside a developer Mac.
  }
}

const browser = await chromium.launch(executablePath ? { executablePath } : {});
const context = await browser.newContext({ deviceScaleFactor: 1, viewport });
const page = await context.newPage();

await page.addInitScript(
  ({ captureMs, titleSelector }) => {
    window.__guardianIntroTimeline = [];
    window.addEventListener("DOMContentLoaded", () => {
      const startedAt = performance.now();
      const sample = (now) => {
        const rails = Object.fromEntries(
          [...document.querySelectorAll("[data-blueprint-rail]")].map((element) => {
            const style = getComputedStyle(element);
            return [
              element.dataset.blueprintRail,
              { opacity: style.opacity, transform: style.transform },
            ];
          }),
        );
        const title = document.querySelector(titleSelector);
        const titleStyle = title ? getComputedStyle(title) : null;
        const scene = document.querySelector(".illumination-document");
        const sceneStyle = scene ? getComputedStyle(scene) : null;
        const header = document.querySelector(".company-home-header");
        const headerStyle = header ? getComputedStyle(header) : null;
        const headerLink = document.querySelector(".company-home-header a");
        const headerLinkStyle = headerLink ? getComputedStyle(headerLink) : null;
        const animations = document
          .getAnimations({ subtree: true })
          .map((animation) => {
            const effect = animation.effect;
            const target = effect?.target;
            if (
              !(target instanceof HTMLElement) ||
              (!target.dataset.blueprintRail && !target.classList.contains("illumination-document"))
            )
              return null;
            return {
              currentTime: typeof animation.currentTime === "number" ? animation.currentTime : null,
              name: getComputedStyle(target).animationName,
              playState: animation.playState,
              rail: target.dataset.blueprintRail ?? null,
            };
          })
          .filter(Boolean);

        window.__guardianIntroTimeline.push({
          animations,
          header: headerStyle
            ? {
                animation: headerStyle.animationName,
                opacity: headerStyle.opacity,
                transform: headerStyle.transform,
                transitionDuration: headerStyle.transitionDuration,
                transitionProperty: headerStyle.transitionProperty,
              }
            : null,
          headerLink: headerLinkStyle
            ? {
                animation: headerLinkStyle.animationName,
                transitionDuration: headerLinkStyle.transitionDuration,
                transitionProperty: headerLinkStyle.transitionProperty,
              }
            : null,
          rails,
          scene: sceneStyle
            ? {
                illumination: sceneStyle.getPropertyValue("--company-illumination").trim(),
                railProgress: sceneStyle.getPropertyValue("--company-rail-progress").trim(),
              }
            : null,
          time: now - startedAt,
          title: titleStyle
            ? { opacity: titleStyle.opacity, transform: titleStyle.transform }
            : null,
        });
        if (now - startedAt < captureMs - 50) requestAnimationFrame(sample);
      };
      requestAnimationFrame(sample);
    });
  },
  { captureMs, titleSelector },
);

const cdp = await context.newCDPSession(page);
const frames = [];
cdp.on("Page.screencastFrame", (event) => {
  frames.push({ data: event.data, timestamp: event.metadata.timestamp });
  void cdp.send("Page.screencastFrameAck", { sessionId: event.sessionId });
});

await page.goto(targetUrl, { waitUntil: "commit" });
await cdp.send("Page.startScreencast", {
  everyNthFrame: 1,
  format: "png",
  maxHeight: viewport.height,
  maxWidth: viewport.width,
});
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(captureMs);
await cdp.send("Page.stopScreencast");

frames.sort((a, b) => a.timestamp - b.timestamp);
const firstTimestamp = frames.at(0)?.timestamp ?? 0;
const capturedFrames = frames.map((frame, index) => {
  const elapsedMs = Math.round((frame.timestamp - firstTimestamp) * 1_000);
  return {
    elapsedMs,
    filename: `${String(index).padStart(4, "0")}-${String(elapsedMs).padStart(4, "0")}ms.png`,
    frame,
  };
});
await Promise.all(
  capturedFrames.map(async ({ filename, frame }) => {
    await fs.writeFile(path.join(outputDir, filename), Buffer.from(frame.data, "base64"));
  }),
);

const timeline = await page.evaluate(() => window.__guardianIntroTimeline ?? []);
await fs.writeFile(
  path.join(outputDir, "manifest.json"),
  `${JSON.stringify(
    {
      base,
      captureMs,
      frames: capturedFrames.map(({ elapsedMs, filename }) => ({ elapsedMs, filename })),
      profileName,
      targetUrl,
      timeline,
      titleSelector,
      viewport,
    },
    null,
    2,
  )}\n`,
);

await browser.close();
console.log(`${profileName}: captured ${capturedFrames.length} compositor frames in ${outputDir}`);

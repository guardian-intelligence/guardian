import fs from "node:fs/promises";
import path from "node:path";
import { chromium } from "@playwright/test";

const base = process.env.BASE ?? "http://127.0.0.1:4252";
const targetUrl = process.env.TARGET_URL ?? `${base}/`;
const titleSelector = process.env.TITLE_SELECTOR ?? ".company-home-title__outline";
const captureMs = Number.parseInt(process.env.CAPTURE_MS ?? "6250", 10);
const reducedMotion = process.env.REDUCED_MOTION === "1" ? "reduce" : "no-preference";
const profileName = process.argv[2] ?? "mobile";
const outputRoot = process.argv[3] ?? "/tmp/company-home-intro";
const profiles = {
  narrow: { width: 320, height: 800 },
  mobile: { width: 390, height: 844 },
  medium: { width: 754, height: 1_000 },
  tablet: { width: 1_024, height: 900 },
  desktop: { width: 1440, height: 900 },
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
const context = await browser.newContext({ deviceScaleFactor: 1, reducedMotion, viewport });
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
        const titleCanvas = document.querySelector("[data-title-materialization]");
        const titleLuminosity = document.querySelector("[data-title-luminosity]");
        const titleRoot = document.querySelector(".company-home-title");
        const titleRootStyle = titleRoot ? getComputedStyle(titleRoot) : null;
        const titleLuminosityStyle = titleLuminosity ? getComputedStyle(titleLuminosity) : null;
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
                beacon: sceneStyle.getPropertyValue("--company-beacon-opacity").trim(),
                copy: sceneStyle.getPropertyValue("--company-copy-opacity").trim(),
                eyebrow: sceneStyle.getPropertyValue("--company-eyebrow-opacity").trim(),
                ambient: sceneStyle.getPropertyValue("--company-ambient-light").trim(),
                materialize: sceneStyle.getPropertyValue("--company-materialize-progress").trim(),
                node: sceneStyle.getPropertyValue("--company-node-opacity").trim(),
                pencil: sceneStyle.getPropertyValue("--company-pencil-opacity").trim(),
                railProgress: sceneStyle.getPropertyValue("--company-rail-progress").trim(),
              }
            : null,
          time: now - startedAt,
          title: titleStyle
            ? { opacity: titleStyle.opacity, transform: titleStyle.transform }
            : null,
          titlePixels:
            titleCanvas instanceof HTMLCanvasElement
              ? {
                  off: titleCanvas.dataset.pixelsOff ?? null,
                  on: titleCanvas.dataset.pixelsOn ?? null,
                  opacity: titleCanvas.dataset.pixelOpacity ?? null,
                  progress: titleCanvas.dataset.materializeProgress ?? null,
                  spotlight: titleCanvas.dataset.pixelsSpotlight ?? null,
                  state: titleCanvas.dataset.titleMaterialization ?? null,
                  total: titleCanvas.dataset.pixelCount ?? null,
                }
              : null,
          titleSpotlight:
            titleRootStyle && titleLuminosityStyle
              ? {
                  left: titleRootStyle.getPropertyValue("--company-spotlight-left").trim(),
                  opacity: titleLuminosityStyle.opacity,
                  right: titleRootStyle.getPropertyValue("--company-spotlight-right").trim(),
                  width: titleRootStyle.getPropertyValue("--company-spotlight-width").trim(),
                }
              : null,
          titleShimmer:
            titleLuminosity instanceof HTMLCanvasElement
              ? {
                  active: titleLuminosity.dataset.shimmerActive ?? null,
                  pixels: titleLuminosity.dataset.shimmerPixels ?? null,
                  progress: titleLuminosity.dataset.shimmerProgress ?? null,
                }
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
const fontRequests = [];
const fontRequestDetails = [];
page.on("request", (request) => {
  if (request.resourceType() === "font" || /\.woff2?(?:$|\?)/.test(request.url())) {
    fontRequests.push(request.url());
  }
});
cdp.on("Network.requestWillBeSent", (event) => {
  if (event.type === "Font" || /\.woff2?(?:$|\?)/.test(event.request.url)) {
    fontRequestDetails.push({ initiator: event.initiator, url: event.request.url });
  }
});
cdp.on("Page.screencastFrame", (event) => {
  frames.push({ data: event.data, timestamp: event.metadata.timestamp });
  void cdp.send("Page.screencastFrameAck", { sessionId: event.sessionId });
});

await cdp.send("Network.enable");
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
const experience = await page.evaluate(() => ({
  canvasFrameCount:
    document.querySelector(".illumination-canvas")?.getAttribute("data-frame-count") ?? null,
  canvasMode: document.documentElement.dataset.canvasMode ?? null,
  mode: document.documentElement.dataset.companyExperience ?? null,
  reason: document.documentElement.dataset.companyExperienceReason ?? null,
  visualIntegrity: document.documentElement.dataset.companyVisualIntegrity ?? null,
}));
const layout = await page.evaluate((selector) => {
  const title = document.querySelector(selector);
  const frame = document.querySelector(".company-home-hero__copy-frame");
  if (!(title instanceof Element) || !(frame instanceof HTMLElement)) return null;

  const titleInk = title.getBoundingClientRect();

  return {
    fontSize: Number.parseFloat(getComputedStyle(frame).fontSize),
    frameWidth: frame.getBoundingClientRect().width,
    hasHorizontalOverflow: document.documentElement.scrollWidth > window.innerWidth,
    titleInkWidth: titleInk.width,
    titleToFrameRatio: titleInk.width / frame.getBoundingClientRect().width,
    titleToViewportRatio: titleInk.width / window.innerWidth,
  };
}, titleSelector);
await fs.writeFile(
  path.join(outputDir, "manifest.json"),
  `${JSON.stringify(
    {
      base,
      captureMs,
      experience,
      frames: capturedFrames.map(({ elapsedMs, filename }) => ({ elapsedMs, filename })),
      fontRequestDetails,
      fontRequests,
      layout,
      profileName,
      reducedMotion,
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

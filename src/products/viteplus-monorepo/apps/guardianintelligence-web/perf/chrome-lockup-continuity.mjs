import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
import { chromium, firefox } from "@playwright/test";

const base = process.env.BASE ?? "http://127.0.0.1:4252";
const outputDir = path.resolve(process.env.OUTPUT_DIR ?? "/tmp/guardian-lockup-continuity");
const chromeExecutable = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";

const viewports = [
  { name: "iphone", width: 390, height: 844 },
  { name: "ipad", width: 820, height: 1_180 },
  { name: "desktop", width: 1_440, height: 900 },
];

const routes = [
  {
    name: "home",
    path: "/",
    navLabel: "Home",
    design: "argent",
    waitSelector: '.persistent-company-home[data-company-home-active="true"]',
  },
  {
    name: "letters",
    path: "/letters",
    navLabel: "Letters",
    design: "chip",
    waitSelector: '[data-treatment="letters"]',
  },
  {
    name: "newsroom",
    path: "/news",
    navLabel: "News",
    design: "emboss",
    waitSelector: '[data-treatment="newsroom"]',
  },
];

const tolerancePx = 0.25;
await fs.mkdir(outputDir, { recursive: true });

const chromeLaunchOptions = {};
try {
  await fs.access(chromeExecutable);
  chromeLaunchOptions.executablePath = chromeExecutable;
} catch {
  // CI uses Playwright's pinned Chromium when Google Chrome is unavailable.
}

const browsers = [
  {
    name: chromeLaunchOptions.executablePath ? "chrome" : "chromium",
    browser: chromium,
    launch: chromeLaunchOptions,
  },
  { name: "firefox", browser: firefox, launch: {} },
];

const summary = [];

for (const engine of browsers) {
  const browser = await engine.browser.launch(engine.launch);
  try {
    for (const viewport of viewports) {
      const context = await browser.newContext({
        viewport: { width: viewport.width, height: viewport.height },
        reducedMotion: "reduce",
      });
      const page = await context.newPage();
      try {
        await page.goto(`${base}/`, { waitUntil: "networkidle" });
        await settle(page);

        const home = await readLockup(page);
        assert.equal(home.design, routes[0].design);
        await capture(page, engine.name, viewport.name, routes[0].name);
        const routeResults = [{ route: routes[0].name, ...home }];
        let canonical;

        for (const route of routes.slice(1)) {
          const samplesPromise = sampleTransition(page);
          await navigate(page, route.navLabel, viewport.width);
          await page.waitForURL(`${base}${route.path}`);
          await page.locator(route.waitSelector).waitFor({ state: "visible" });
          const samples = await samplesPromise;
          await settle(page);

          for (const sample of samples) {
            assertClose(
              sample.left,
              home.left,
              `${engine.name}/${viewport.name}/${route.name} transition left`,
            );
            assertClose(
              sample.top,
              home.top,
              `${engine.name}/${viewport.name}/${route.name} transition top`,
            );
          }

          const current = await readLockup(page);
          assert.equal(current.design, route.design);
          assertClose(
            current.left,
            home.left,
            `${engine.name}/${viewport.name}/${route.name} left`,
          );
          assertClose(current.top, home.top, `${engine.name}/${viewport.name}/${route.name} top`);
          if (route.name === "letters") {
            canonical = current;
            assertTypography(home, canonical, `${engine.name}/${viewport.name}/home`);
          } else {
            assertTypography(current, canonical, `${engine.name}/${viewport.name}/${route.name}`);
          }
          await capture(page, engine.name, viewport.name, route.name);
          routeResults.push({ route: route.name, ...current, transitionSamples: samples.length });
        }

        const homeSamplesPromise = sampleTransition(page);
        await navigate(page, routes[0].navLabel, viewport.width);
        await page.waitForURL(`${base}/`);
        await page.locator(routes[0].waitSelector).waitFor({ state: "visible" });
        const homeSamples = await homeSamplesPromise;
        await settle(page);
        const returned = await readLockup(page);
        for (const sample of homeSamples) {
          assertClose(
            sample.left,
            home.left,
            `${engine.name}/${viewport.name}/return transition left`,
          );
          assertClose(
            sample.top,
            home.top,
            `${engine.name}/${viewport.name}/return transition top`,
          );
        }
        assert.equal(returned.design, routes[0].design);
        assertClose(returned.left, home.left, `${engine.name}/${viewport.name}/return left`);
        assertClose(returned.top, home.top, `${engine.name}/${viewport.name}/return top`);
        assertTypography(returned, canonical, `${engine.name}/${viewport.name}/returned home`);

        summary.push({
          engine: engine.name,
          viewport: viewport.name,
          canonical,
          routes: routeResults,
        });
      } finally {
        await context.close();
      }
    }
  } finally {
    await browser.close();
  }
}

await fs.writeFile(
  path.join(outputDir, "measurements.json"),
  `${JSON.stringify(summary, null, 2)}\n`,
);
console.log(JSON.stringify({ outputDir, runs: summary.length, tolerancePx }));

async function settle(page) {
  await page.evaluate(async () => {
    await document.fonts.ready;
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  });
}

async function navigate(page, label, viewportWidth) {
  if (viewportWidth < 768) {
    await page.getByRole("button", { name: "Open menu" }).click();
  }
  await page.getByRole("link", { exact: true, name: label }).click();
}

async function readLockup(page) {
  return page.evaluate(() => {
    const links = [...document.querySelectorAll("header a[aria-label^='Guardian —']")];
    const link = links.find((candidate) => {
      const style = getComputedStyle(candidate);
      const rect = candidate.getBoundingClientRect();
      return (
        !candidate.closest("[inert]") &&
        style.display !== "none" &&
        style.visibility === "visible" &&
        rect.width > 0 &&
        rect.height > 0
      );
    });
    if (!link) throw new Error("visible Guardian lockup not found");
    const rect = link.getBoundingClientRect();
    const lockup = link.querySelector("[data-lockup]");
    const wordmark = link.querySelector("[data-lockup-wordmark]");
    if (!lockup || !wordmark) throw new Error("canonical Guardian lockup structure not found");
    const lockupRect = lockup.getBoundingClientRect();
    const wordmarkRect = wordmark.getBoundingClientRect();
    const wordmarkStyle = getComputedStyle(wordmark);
    return {
      design: lockup.getAttribute("data-variant"),
      left: rect.left,
      top: rect.top,
      lockupHeight: lockupRect.height,
      wordmarkLeft: wordmarkRect.left,
      wordmarkTop: wordmarkRect.top,
      typography: {
        fontFamily: wordmarkStyle.fontFamily,
        fontSize: wordmarkStyle.fontSize,
        fontWeight: wordmarkStyle.fontWeight,
        letterSpacing: wordmarkStyle.letterSpacing,
        lineHeight: wordmarkStyle.lineHeight,
        textTransform: wordmarkStyle.textTransform,
        transform: wordmarkStyle.transform,
      },
    };
  });
}

async function sampleTransition(page) {
  return page.evaluate(
    () =>
      new Promise((resolve) => {
        const samples = [];
        const startedAt = performance.now();
        const sample = () => {
          const links = [...document.querySelectorAll("header a[aria-label^='Guardian —']")];
          for (const link of links) {
            const style = getComputedStyle(link);
            const rect = link.getBoundingClientRect();
            if (
              !link.closest("[inert]") &&
              style.display !== "none" &&
              style.visibility === "visible" &&
              rect.width > 0 &&
              rect.height > 0
            ) {
              samples.push({ left: rect.left, top: rect.top });
            }
          }
          if (performance.now() - startedAt < 450) requestAnimationFrame(sample);
          else resolve(samples);
        };
        requestAnimationFrame(sample);
      }),
  );
}

async function capture(page, engine, viewport, route) {
  await page.screenshot({
    path: path.join(outputDir, `${engine}-${viewport}-${route}.png`),
    animations: "disabled",
  });
}

function assertClose(actual, expected, label) {
  assert(
    Math.abs(actual - expected) <= tolerancePx,
    `${label}: expected ${expected}px ± ${tolerancePx}px, got ${actual}px`,
  );
}

function assertTypography(actual, canonical, label) {
  assert.deepEqual(
    actual.typography,
    canonical.typography,
    `${label} typography must match Letters`,
  );
  assertClose(actual.lockupHeight, canonical.lockupHeight, `${label} lockup height`);
  assertClose(actual.wordmarkLeft, canonical.wordmarkLeft, `${label} wordmark left`);
  assertClose(actual.wordmarkTop, canonical.wordmarkTop, `${label} wordmark top`);
}

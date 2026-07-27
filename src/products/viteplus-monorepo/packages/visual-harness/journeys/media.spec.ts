import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import { loadCanaryConfig } from "../src/config.ts";

const cfg = loadCanaryConfig(process.env);

// 1.5 seconds of 64x64 VP8 in a WebM container. Keeping the fixture inline
// avoids a writable-file dependency in the read-only canary image.
const WEBM_FIXTURE = Buffer.from(
  "GkXfo59ChoEBQveBAULygQRC84EIQoKEd2VibUKHgQJChYECGFOAZwH/////////EU2bdKtNu4tTq4QVSalmU6yBoU27i1OrhBZUrmtTrIHLTbuMU6uEElTDZ1OsggEY7AEAAAAAAABoAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAVSalmpSrXsYMPQkBNgIxMYXZmNjEuOS4xMDBXQYxMYXZmNjEuOS4xMDAWVK5ryK4BAAAAAAAAP9eBAXPFiF7R0a74j01cnIEAIrWcg3VuZIiBAIaFVl9WUDiDgQEj44OEDuaygOCQsIFAuoFAmoECVbCEVbmBARJUw2fXc3OfY8CAZ8iZRaOHRU5DT0RFUkSHjExhdmY2MS45LjEwMHNzsmPAi2PFiF7R0a74j01cZ8ihRaOHRU5DT0RFUkSHlExhdmM2MS4yMC4xMDAgbGlidnB4H0O2dUCo54EAo8OBAACAkAMAnQEqQABAAABHCIWFiIWEiAICAnWqA/gCBuhBXDHSEwBVWAD+/00S//xYV/FhX8WFf/FhX/z8zu3F/OYAo5aBAPoA0QEAARAQABgAGFgv9AAIjoAAo5aBAfQA0QEAARAQABgAGFgv9AAIjoAAo5aBAu4A0QEAARAQABgAGFgv9AAIjoAAo5aBA+gA0QEAARAQABgAGFgv9AAIjoAAH0O2dZznggTio5aBAAAA0QEAARAQABgAGFgv9AAIjoAA",
  "base64",
);

// Three minutes of sparse 1280x720 VP8. The long timeline is real container
// time, while six static frames keep the fixture small.
const LONG_WEBM_FIXTURE = Buffer.from(
  readFileSync(new URL("./fixtures/privatecut-180s.webm.base64", import.meta.url), "utf8"),
  "base64",
);

if (cfg.target.name === "privatecut") {
  test("three-minute selections show a reactive quality warning", async ({ page }) => {
    test.setTimeout(60_000);
    await page.goto(cfg.targetUrl, { waitUntil: "load" });
    await page.locator('input[type="file"]').setInputFiles({
      name: "privatecut-three-minutes.webm",
      mimeType: "video/webm",
      buffer: LONG_WEBM_FIXTURE,
    });

    await expect(page.getByText("0:00 – 3:00", { exact: true })).toBeVisible();
    const warning = page.getByRole("status").filter({ hasText: "Low-quality output likely" });
    await expect(warning).toContainText(
      "Try increasing maximum size or splitting into shorter clips.",
    );

    const outputGroup = page.getByRole("group", { name: "Output format" });
    await outputGroup.getByRole("button", { name: "WebM" }).click();
    await expect(warning).toBeHidden();
    await outputGroup.getByRole("button", { name: "MP4" }).click();
    await expect(warning).toBeVisible();
  });

  test("WebM input exports measured MP4 and WebM artifacts", async ({ page }) => {
    test.setTimeout(60_000);
    await page.goto(cfg.targetUrl, { waitUntil: "load" });
    await page.locator('input[type="file"]').setInputFiles({
      name: "privatecut-canary.webm",
      mimeType: "video/webm",
      buffer: WEBM_FIXTURE,
    });

    const outputGroup = page.getByRole("group", { name: "Output format" });
    await expect(outputGroup).toBeVisible();
    await expect(outputGroup.getByRole("button", { name: "MP4" })).toBeEnabled();
    await expect(outputGroup.getByRole("button", { name: "WebM" })).toBeEnabled();

    // Move off the source keyframe so both assertions exercise decoding and
    // encoding, not only the lossless remux path.
    await page.getByRole("checkbox", { name: "snap to keyframes" }).uncheck();
    const timeline = page.getByRole("slider", { name: "Selection start" });
    const box = await timeline.boundingBox();
    if (box === null) throw new Error("selection start handle has no bounding box");
    const strip = timeline.locator("xpath=..");
    const stripBox = await strip.boundingBox();
    if (stripBox === null) throw new Error("timeline has no bounding box");
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down();
    await page.mouse.move(stripBox.x + stripBox.width * 0.25, box.y + box.height / 2);
    await page.mouse.up();

    await assertExport(page, "MP4", ".mp4", "video/mp4", "66747970");
    await page.getByRole("button", { name: "Adjust selection" }).click();
    await assertExport(page, "WebM", ".webm", "video/webm", "1a45dfa3");
  });
}

async function assertExport(
  page: import("@playwright/test").Page,
  label: "MP4" | "WebM",
  extension: ".mp4" | ".webm",
  mimeType: "video/mp4" | "video/webm",
  magic: string,
): Promise<void> {
  await page
    .getByRole("group", { name: "Output format" })
    .getByRole("button", { name: label })
    .click();
  await page.getByRole("button", { name: "Create clip" }).click();
  const download = page.getByRole("link", { name: "Download" });
  await expect(download).toBeVisible({ timeout: 30_000 });
  await expect(download).toHaveAttribute(
    "download",
    new RegExp(`${extension.replace(".", "\\.")}$`),
  );

  const artifact = await download.evaluate(async (link) => {
    const response = await fetch((link as HTMLAnchorElement).href);
    const bytes = new Uint8Array(await response.arrayBuffer());
    return {
      mimeType: response.headers.get("content-type"),
      size: bytes.byteLength,
      head: [...bytes.slice(0, 16)].map((byte) => byte.toString(16).padStart(2, "0")).join(""),
    };
  });
  expect(artifact.mimeType).toBe(mimeType);
  expect(artifact.size).toBeGreaterThan(0);
  expect(artifact.size).toBeLessThanOrEqual(4_000_000);
  expect(artifact.head).toContain(magic);
}

// Screenshot the phone, WebGPU on: node scripts/shot.mjs [frame ...]
// Writes shots/<frame>.png (2x). Needs `vp dev` running on :4255.
import { mkdir } from "node:fs/promises";

import { chromium } from "@playwright/test";

const frames = process.argv.slice(2);
const out = new URL("../shots/", import.meta.url);
await mkdir(out, { recursive: true });
const browser = await chromium.launch({
  channel: "chrome",
  args: ["--enable-unsafe-webgpu", "--enable-features=Vulkan"],
});
const page = await browser.newPage({
  viewport: { width: 1000, height: 920 },
  deviceScaleFactor: 2,
});
page.on("console", (m) => {
  if (m.type() === "error" || m.type() === "warning") console.log(`[${m.type()}] ${m.text()}`);
});
page.on("pageerror", (e) => console.log(`[pageerror] ${e.message}`));
for (const frame of frames.length ? frames : ["live"]) {
  await page.goto(`http://127.0.0.1:4255/?frame=${frame}`, { waitUntil: "networkidle" });
  await page.waitForTimeout(4000);
  const gpu = await page.evaluate(() => document.querySelector(".phone")?.className);
  console.log(frame, gpu);
  const phone = page.locator(".phone");
  await phone.screenshot({ path: new URL(`${frame}.png`, out).pathname });
  // 1:1 close-up of the floating glass controls
  const box = await phone.boundingBox();
  if (box)
    await page.screenshot({
      path: new URL(`${frame}-glass.png`, out).pathname,
      clip: { x: box.x, y: box.y + box.height - 190, width: box.width, height: 190 },
    });
}
await browser.close();

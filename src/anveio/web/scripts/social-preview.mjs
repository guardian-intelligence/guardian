// Run from the workspace root: vp run @guardian/anveio-web#social-preview.
import { createHash } from "node:crypto";
import { spawn } from "node:child_process";
import { writeFileSync, unlinkSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { chromium } from "@playwright/test";
import { previewSourceHash } from "./preview-source.mjs";

const root = new URL("../", import.meta.url);
try {
  await fetch("http://127.0.0.1:4264/deck");
  throw new Error("Port 4264 is occupied; stop that server before generating the preview");
} catch (error) {
  if (error instanceof Error && error.message.startsWith("Port 4264")) throw error;
}
const server = spawn(
  process.execPath,
  [fileURLToPath(new URL("node_modules/vite-plus/bin/vp", root)), "dev", "--port", "4264"],
  {
    cwd: fileURLToPath(root),
    stdio: "inherit",
    detached: true,
  },
);
let browser;
try {
  for (let attempt = 0; ; attempt++) {
    try {
      if ((await fetch("http://127.0.0.1:4264/deck")).ok) break;
    } catch {
      /* Wait for the local source server. */
    }
    if (attempt >= 120 || server.exitCode !== null)
      throw new Error("Preview server did not become ready");
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  browser = await chromium.launch();
  const page = await browser.newPage({
    viewport: { width: 1200, height: 630 },
    deviceScaleFactor: 1,
  });
  await page.goto("http://127.0.0.1:4264/deck");
  // Keep the actual 16:9 slide intact inside the social card's 1200×630 canvas.
  // Only the surrounding desk and responsive scaling are changed.
  await page.addStyleTag({
    content: `
    :root { --deck-room-w: 1120px; --deck-room-h: 630px; --deck-scale: .875; }
    html, body { margin: 0; background: #000; }
    .deck { padding: 0; gap: 0; }
    .slide:not(#title) { display: none; }
    #title .slide-inner { box-shadow: none; }
  `,
  });
  await page.evaluate(async () => {
    await document.fonts.ready;
  });
  await page.locator("#title .title-hero").waitFor();
  const png = await page.screenshot({ animations: "disabled" });
  const digest = createHash("sha256").update(png).digest("hex");
  const image = `/deck/social/title-${digest.slice(0, 16)}.png`;
  const { mkdirSync, readFileSync } = await import("node:fs");
  mkdirSync(new URL("public/deck/social/", root), { recursive: true });
  const manifest = new URL("src/deck/social-preview.json", root);
  let previous;
  try {
    previous = JSON.parse(readFileSync(manifest, "utf8"));
  } catch {
    /* First generation. */
  }
  writeFileSync(new URL(`public${image}`, root), png);
  writeFileSync(
    manifest,
    JSON.stringify(
      { image, width: 1200, height: 630, sha256: digest, sourceHash: previewSourceHash() },
      null,
      2,
    ) + "\n",
  );
  if (previous?.image && previous.image !== image)
    unlinkSync(new URL(`public${previous.image}`, root));
  console.log(`Generated ${image} from the real title slide.`);
} finally {
  await browser?.close();
  if (server.pid) process.kill(-server.pid, "SIGTERM");
}

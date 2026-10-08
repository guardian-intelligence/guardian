// Drive the Google sign-in state machine end to end with Google stood in by
// Playwright: the consent page answers at once, signed by a local RSA key
// served in place of Google's published keys. Needs `vp dev` on :4255.
// node scripts/oauth-check.mjs <outdir>
import { createSign, generateKeyPairSync } from "node:crypto";

import { chromium } from "@playwright/test";

const out = process.argv[2] ?? "shots";
const { privateKey, publicKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
const jwk = { ...publicKey.export({ format: "jwk" }), kid: "test", alg: "RS256", use: "sig" };
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
const sign = (claims, key = privateKey) => {
  const head = `${b64({ alg: "RS256", kid: "test", typ: "JWT" })}.${b64(claims)}`;
  return `${head}.${createSign("RSA-SHA256").update(head).sign(key).toString("base64url")}`;
};

const browser = await chromium.launch({ channel: "chrome" });
const ctx = await browser.newContext({
  viewport: { width: 1000, height: 920 },
  deviceScaleFactor: 2,
});
// what the fake Google does with each request: "ok", "deny", "forge", "hang"
let mode = "ok";
await ctx.route("https://www.googleapis.com/oauth2/v3/certs", (r) =>
  r.fulfill({ json: { keys: [jwk] }, headers: { "access-control-allow-origin": "*" } }),
);
await ctx.route("https://accounts.google.com/**", (r) => {
  const q = new URL(r.request().url()).searchParams;
  const back = q.get("redirect_uri");
  const state = q.get("state");
  if (mode === "hang") return r.fulfill({ contentType: "text/html", body: "<p>Google…</p>" });
  const now = Math.floor(Date.now() / 1000);
  const claims = {
    iss: "https://accounts.google.com",
    aud: q.get("client_id"),
    sub: "1234",
    email: "samantha@example.com",
    name: "Samantha Example",
    nonce: q.get("nonce"),
    iat: now,
    exp: now + 3600,
  };
  const forged = generateKeyPairSync("rsa", { modulusLength: 2048 }).privateKey;
  const frag =
    mode === "deny"
      ? `error=access_denied&state=${state}`
      : `id_token=${sign(claims, mode === "forge" ? forged : privateKey)}&state=${state}`;
  return r.fulfill({
    contentType: "text/html",
    body: `<script>setTimeout(()=>location.replace(${JSON.stringify(back)}+"#${frag}"),900)</script>`,
  });
});

const page = await ctx.newPage();
page.on("pageerror", (e) => console.log(`[pageerror] ${e.message}`));
await page.goto("http://127.0.0.1:4255/", { waitUntil: "networkidle" });
await page.waitForTimeout(2500);
const box = await page.locator(".phone").boundingBox();
const bottom = (name) =>
  page.screenshot({
    path: `${out}/${name}.png`,
    clip: { x: box.x, y: box.y + box.height - 220, width: box.width, height: 220 },
  });
const report = async (label) => {
  const s = await page.evaluate(() => ({
    card: document.querySelector(".signin-label")?.textContent ?? null,
    line: document.querySelector(".bubble [role=status]")?.textContent ?? null,
    field: document.querySelector(".field-tap")?.textContent ?? null,
  }));
  console.log(label.padEnd(10), JSON.stringify(s));
};
const tapCard = async () => {
  const c = await page.locator(".stack-front").boundingBox();
  const popup = ctx.waitForEvent("page", { timeout: 3000 }).catch(() => null);
  await page.mouse.click(c.x + 40, c.y + c.height / 2);
  return popup;
};

await page.getByRole("button", { name: "Leave a message" }).click();
await page.waitForTimeout(2200);
await report("signin");

for (const m of ["deny", "forge"]) {
  mode = m;
  await tapCard();
  await page.waitForTimeout(400);
  await report(`${m}:wait`);
  await page.waitForTimeout(2600);
  await report(`${m}:done`);
}

mode = "hang";
const p = await tapCard();
await page.waitForTimeout(300);
await bottom("oauth-waiting");
await p?.close();
await page.waitForTimeout(2600);
await report("closed");

mode = "ok";
await tapCard();
await page.waitForTimeout(400);
await report("ok:wait");
await page.waitForTimeout(3000);
await report("ok:done");
await bottom("oauth-signed-in");
await browser.close();

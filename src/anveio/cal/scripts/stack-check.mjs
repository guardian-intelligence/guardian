// Drive the card stack: leave a message signed out, swipe Sign in away,
// bring it back, reorder it, and sign in. node scripts/stack-check.mjs <outdir>
import { chromium } from "@playwright/test";

const out = process.argv[2] ?? "shots";
const browser = await chromium.launch({ channel: "chrome", args: ["--enable-unsafe-webgpu"] });
const page = await browser.newPage({
  viewport: { width: 1000, height: 920 },
  deviceScaleFactor: 2,
});
page.on("pageerror", (e) => console.log(`[pageerror] ${e.message}`));
page.on("console", (m) => m.type() === "error" && console.log(`[error] ${m.text()}`));
await page.goto("http://127.0.0.1:4255/", { waitUntil: "networkidle" });
await page.waitForTimeout(3000);
const phone = page.locator(".phone");
const box = await phone.boundingBox();
const bottom = (name) =>
  page.screenshot({
    path: `${out}/${name}.png`,
    clip: { x: box.x, y: box.y + box.height - 220, width: box.width, height: 220 },
  });
const cards = () =>
  page.evaluate(() =>
    [...document.querySelectorAll(".stack-card")].map((c) => c.className.split(" ").join(".")),
  );

await page.getByRole("button", { name: "Leave a message" }).click();
await page.waitForTimeout(2600);
await bottom("1-signin");
console.log("after leave", await cards());

const front = await page.locator(".stack-front").boundingBox();
const y = front.y + front.height / 2;
await page.mouse.move(front.x + 30, y);
await page.mouse.down();
for (let i = 1; i <= 8; i++) await page.mouse.move(front.x + 30 + i * 12, y);
await bottom("2-mid-swipe");
for (let i = 9; i <= 20; i++) await page.mouse.move(front.x + 30 + i * 12, y);
await page.mouse.up();
await page.waitForTimeout(600);
await bottom("3-dismissed");
console.log("after swipe", await cards());

// a swipe on "Leave a message" only rubber-bands
const f2 = await page.locator(".stack-front").boundingBox();
await page.mouse.move(f2.x + 30, f2.y + 20);
await page.mouse.down();
for (let i = 1; i <= 20; i++) await page.mouse.move(f2.x + 30 + i * 12, f2.y + 20);
await bottom("4-rubber");
await page.mouse.up();
await page.waitForTimeout(500);
console.log("after rubber", await cards());

// Rumi moves cards herself
await page.evaluate(() => window.rumi.stack({ op: "add", card: "signin", at: 1 }));
await page.waitForTimeout(500);
await bottom("5-signin-behind");
console.log("added behind", await cards());
await page.evaluate(() => window.rumi.stack({ op: "move", card: "signin", to: 0 }));
await page.waitForTimeout(500);
await bottom("6-moved-front");
console.log("moved front", await cards());

// anywhere on the card signs in while Google is the only way in
const card = await page.locator(".stack-front").boundingBox();
await page.mouse.click(card.x + 40, card.y + card.height / 2);
await page.waitForTimeout(2600);
await bottom("7-signed-in");
console.log("signed in", await cards());
await browser.close();

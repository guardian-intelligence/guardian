// Objective check of Rumi's candlelight on the message field.
// Needs `vp dev` on :4255. Usage: node scripts/measure-flame.mjs [seconds=24]
//
// 1. Samples the --flame vars every animation frame in the live page.
// 2. Separately screenshots the field's lit end and measures pixel luminance
//    (with the flame slowed 7x), to prove the vars actually reach the screen.
// Prints each metric next to its target and exits 1 if any target misses.
import { inflateSync } from "node:zlib";

import { chromium } from "@playwright/test";

const SECONDS = Number(process.argv[2] ?? 24);
const FPS = 60;

const browser = await chromium.launch({ channel: "chrome", args: ["--enable-unsafe-webgpu"] });
const page = await browser.newPage({
  viewport: { width: 1000, height: 920 },
  deviceScaleFactor: 2,
});
await page.goto("http://127.0.0.1:4255/?frame=booked", { waitUntil: "networkidle" });
await page.evaluate(() => localStorage.clear());
await page.reload({ waitUntil: "networkidle" });
await page.waitForTimeout(1500);

// ---- 1. the signal itself, every frame ----
const samples = await page.evaluate(
  (seconds) =>
    new Promise((resolve) => {
      const root = document.querySelector(".phone");
      const out = [];
      const t0 = performance.now();
      const step = (now) => {
        const cs = getComputedStyle(root);
        out.push([
          (now - t0) / 1000,
          Number(cs.getPropertyValue("--flame")),
          Number(cs.getPropertyValue("--flame-size")),
        ]);
        if (now - t0 < seconds * 1000) requestAnimationFrame(step);
        else resolve(out);
      };
      requestAnimationFrame(step);
    }),
  SECONDS,
);

// ---- 2. what reaches the screen ----
const field = await page.locator(".bar .field").boundingBox();
const clip = { x: field.x + 4, y: field.y + 8, width: 70, height: field.height - 16 };
const light = () =>
  page.evaluate(() => {
    const cs = getComputedStyle(document.querySelector(".phone"));
    return Number(cs.getPropertyValue("--flame")) * Number(cs.getPropertyValue("--flame-size"));
  });
// Capture the field's lit end for `ms`, bracketing each shot with reads of the
// light (brightness x size) and averaging them.
async function capture(ms) {
  const out = [];
  const tStart = Date.now();
  while (Date.now() - tStart < ms) {
    const before = await light();
    const png = await page.screenshot({ clip });
    const after = await light();
    out.push([meanLuma(png), (before + after) / 2]);
  }
  return out;
}
// (a) at real speed: how much the light visibly swings on screen
const live = await capture(12_000);
// (b) fidelity: a screenshot takes tens of ms, longer than the flicker holds
// still, so slow the same flame 7x (a studio draft) to test the pipeline
// rather than the capture's timing.
await page.evaluate(() =>
  localStorage.setItem(
    "anveio-cal:tuning-draft",
    JSON.stringify({ orb: { flameRate: 1 / 7 }, glass: {}, ink: {} }),
  ),
);
await page.reload({ waitUntil: "networkidle" });
await page.waitForTimeout(1000);
const pixels = await capture(10_000);
await page.evaluate(() => localStorage.clear());
await browser.close();

// ---- analysis ----
const resample = (rows, col) => {
  const end = rows.at(-1)[0];
  const n = Math.floor(end * FPS);
  const out = new Float64Array(n);
  let j = 0;
  for (let i = 0; i < n; i++) {
    const t = i / FPS;
    while (j < rows.length - 2 && rows[j + 1][0] < t) j++;
    const [t0, ...a] = rows[j];
    const [t1, ...b] = rows[j + 1];
    const u = Math.min(1, Math.max(0, (t - t0) / (t1 - t0 || 1)));
    out[i] = a[col] + (b[col] - a[col]) * u;
  }
  return out;
};
const b = resample(samples, 0);
const size = resample(samples, 1);
const mean = (x) => x.reduce((s, v) => s + v, 0) / x.length;
const pct = (x, p) => Float64Array.from(x).sort()[Math.floor(p * (x.length - 1))];
const corr = (x, y) => {
  const mx = mean(x);
  const my = mean(y);
  let sxy = 0,
    sxx = 0,
    syy = 0;
  for (let i = 0; i < x.length; i++) {
    sxy += (x[i] - mx) * (y[i] - my);
    sxx += (x[i] - mx) ** 2;
    syy += (y[i] - my) ** 2;
  }
  return sxy / Math.sqrt(sxx * syy);
};

const m = mean(b);
const range = pct(b, 0.95) - pct(b, 0.05);
const std = Math.sqrt(mean(b.map((v) => (v - m) ** 2)));
let maxJump = 0;
let maxAccel = 0;
let rough = 0;
for (let i = 1; i < b.length; i++) maxJump = Math.max(maxJump, Math.abs(b[i] - b[i - 1]));
for (let i = 2; i < b.length; i++) {
  const acc = b[i] - 2 * b[i - 1] + b[i - 2];
  rough += acc ** 2;
  maxAccel = Math.max(maxAccel, Math.abs(acc));
}
rough = Math.sqrt(rough / (b.length - 2)) / range;

// power spectrum (direct DFT of the mean-removed signal, Hann window)
const N = b.length;
const x = b.map((v, i) => (v - m) * (0.5 - 0.5 * Math.cos((2 * Math.PI * i) / (N - 1))));
const power = [];
for (let k = 1; k < N / 2; k++) {
  let re = 0,
    im = 0;
  for (let i = 0; i < N; i++) {
    const a = (2 * Math.PI * k * i) / N;
    re += x[i] * Math.cos(a);
    im -= x[i] * Math.sin(a);
  }
  power.push([(k * FPS) / N, re * re + im * im]);
}
const total = power.reduce((s, [, p]) => s + p, 0);
const band = (lo, hi) =>
  power.filter(([f]) => f >= lo && f < hi).reduce((s, [, p]) => s + p, 0) / total;

// periodicity: a repeating signal's autocorrelation climbs back up after it
// first decorrelates. Take the highest autocorrelation beyond the first zero
// crossing, out to 10 s (1 = perfectly repeating). Plain short-lag correlation
// would just measure slowness.
const ac = (lag) => corr(b.subarray(0, N - lag), b.subarray(lag));
let periodic = 0;
let crossed = false;
for (let lag = 3; lag <= 10 * FPS && lag < N / 2; lag += 3) {
  const r = ac(lag);
  if (!crossed && r < 0) crossed = true;
  if (crossed) periodic = Math.max(periodic, r);
}

// fast dips: drops of >12% of the mean within 0.3 s (gusts and deep flickers)
let gusts = 0;
for (let i = 18, armed = true; i < N; i++) {
  const drop = (b[i - 18] - b[i]) / m;
  if (armed && drop > 0.12) {
    gusts++;
    armed = false;
  }
  if (drop < 0.02) armed = true;
}

const lumas = pixels.map(([l]) => l);
const liveLumas = live.map(([l]) => l);

const rows = [
  [
    "depth (p5-p95 / mean)",
    range / m,
    (v) => v >= 0.4 && v <= 0.9,
    "0.40-0.90: a lot of vacillation, never out",
  ],
  // both layers are judged by absolute size: how far each moves the light
  [
    "slow waxing 0.02-0.6 Hz (RMS % of mean)",
    (Math.sqrt(band(0.02, 0.6)) * std * 100) / m,
    (v) => v >= 15,
    ">= 15% (big, slow breaths)",
  ],
  [
    "flicker 0.6-5 Hz (RMS % of mean)",
    (Math.sqrt(band(0.6, 5)) * std * 100) / m,
    (v) => v >= 6,
    ">= 6% (visibly alive)",
  ],
  ["jitter > 5 Hz", band(5, 31), (v) => v < 0.03, "< 0.03 of variance"],
  // A pop is a discontinuity: a sudden change of slope. Fast but smooth flicker
  // swings are wanted, so the step size only gets a loose ceiling.
  ["largest slope change (% of range)", (maxAccel / range) * 100, (v) => v < 1, "< 1% (no pops)"],
  ["largest 1-frame step (% of range)", (maxJump / range) * 100, (v) => v < 8, "< 8% (no lurches)"],
  ["roughness (RMS 2nd diff / range)", rough, (v) => v < 0.01, "< 0.01 (smooth)"],
  ["periodicity (autocorr after decorrelating)", periodic, (v) => v < 0.5, "< 0.5 (not uniform)"],
  [
    "fast dips (>12% in 0.3 s)",
    gusts,
    (v) => v >= Math.floor(SECONDS / 9),
    `>= ${Math.floor(SECONDS / 9)} in ${SECONDS}s`,
  ],
  ["flame size tracks brightness (r)", corr(b, size), (v) => v > 0.8, "> 0.8"],
  [
    "pixels track brightness x size (r)",
    corr(
      lumas,
      pixels.map(([, f]) => f),
    ),
    (v) => v > 0.85,
    "> 0.85 (flame slowed 7x)",
  ],
  [
    "on-screen luminance depth",
    (pct(liveLumas, 0.95) - pct(liveLumas, 0.05)) / mean(liveLumas),
    (v) => v > 0.15,
    "> 0.15 (visible, real speed)",
  ],
];
let failed = 0;
console.log(
  `${samples.length} frames over ${SECONDS}s (${(samples.length / SECONDS).toFixed(0)} fps), ${live.length} + ${pixels.length} pixel samples\n`,
);
for (const [name, value, ok, target] of rows) {
  const pass = ok(value);
  if (!pass) failed++;
  console.log(
    `${pass ? "PASS" : "FAIL"}  ${name.padEnd(36)} ${value.toFixed(3).padStart(8)}   target ${String(target)}`,
  );
}
process.exit(failed ? 1 : 0);

// Mean Rec. 709 luma of an RGB(A) PNG.
function meanLuma(buf) {
  let pos = 8,
    w = 0,
    h = 0,
    bpp = 4;
  const idat = [];
  while (pos < buf.length) {
    const len = buf.readUInt32BE(pos);
    const type = buf.toString("ascii", pos + 4, pos + 8);
    const body = buf.subarray(pos + 8, pos + 8 + len);
    if (type === "IHDR") {
      w = body.readUInt32BE(0);
      h = body.readUInt32BE(4);
      bpp = body[9] === 6 ? 4 : 3;
    }
    if (type === "IDAT") idat.push(body);
    pos += 12 + len;
  }
  const raw = inflateSync(Buffer.concat(idat));
  const stride = w * bpp;
  let prev = new Uint8Array(stride);
  let sum = 0;
  for (let y = 0, i = 0; y < h; y++) {
    const f = raw[i];
    const line = Uint8Array.from(raw.subarray(i + 1, i + 1 + stride));
    i += 1 + stride;
    for (let k = 0; k < stride; k++) {
      const a = k >= bpp ? line[k - bpp] : 0;
      const up = prev[k];
      const c = k >= bpp ? prev[k - bpp] : 0;
      if (f === 1) line[k] = (line[k] + a) & 255;
      else if (f === 2) line[k] = (line[k] + up) & 255;
      else if (f === 3) line[k] = (line[k] + ((a + up) >> 1)) & 255;
      else if (f === 4) {
        const p = a + up - c;
        const pa = Math.abs(p - a),
          pb = Math.abs(p - up),
          pc = Math.abs(p - c);
        line[k] = (line[k] + (pa <= pb && pa <= pc ? a : pb <= pc ? up : c)) & 255;
      }
    }
    for (let k = 0; k < stride; k += bpp)
      sum += 0.2126 * line[k] + 0.7152 * line[k + 1] + 0.0722 * line[k + 2];
    prev = line;
  }
  return sum / (w * h);
}

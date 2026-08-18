// Frame-timing sampler for a fixed wall-clock window, started BEFORE a
// caller-driven event (a click, a navigation) and read out after. Unlike
// scroll-measure.mjs (which drives its own scroll loop inside one atomic
// page.evaluate), this splits into install/read so the caller can trigger
// something from the Playwright side (a real click) in between.
export async function startFrameSample(page) {
  await page.evaluate(() => {
    window.__frameSample = { deltas: [], longtasks: [] };
    try {
      new PerformanceObserver((l) =>
        l.getEntries().forEach((e) => window.__frameSample.longtasks.push(Math.round(e.duration))),
      ).observe({ type: "longtask" });
    } catch {
      // longtask observer is Chromium-only; frame deltas still tell the story
    }
    let last = performance.now();
    const tick = (t) => {
      window.__frameSample.deltas.push(t - last);
      last = t;
      if (window.__frameSample) requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  });
}

export async function readFrameSample(page) {
  return page.evaluate(() => {
    const { deltas, longtasks } = window.__frameSample;
    window.__frameSample = undefined; // stop the rAF loop
    const sorted = [...deltas].sort((a, b) => a - b);
    const q = (f) => sorted[Math.floor(f * (sorted.length - 1))];
    return {
      frames: deltas.length,
      avg: +(deltas.reduce((s, d) => s + d, 0) / deltas.length).toFixed(1),
      p50: +q(0.5).toFixed(1),
      p95: +q(0.95).toFixed(1),
      max: +q(1).toFixed(1),
      "jank>33ms": deltas.filter((d) => d > 33.4).length,
      longtasks: longtasks.length,
    };
  });
}

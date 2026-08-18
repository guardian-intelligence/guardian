// Shared 5s triangle-scroll frame-timing measurement, used by both
// scroll-jank.mjs (manual ablation exploration) and scroll-jank-gate.mjs (CI
// promotion gate). One measurement method, so a methodology change can't
// silently drift the two apart.
export async function measureScrollJank(page) {
  return page.evaluate(async () => {
    scrollTo(0, 0);
    await new Promise((r) => setTimeout(r, 300));
    const deltas = [];
    const longtasks = [];
    try {
      new PerformanceObserver((l) =>
        l.getEntries().forEach((e) => longtasks.push(Math.round(e.duration))),
      ).observe({ type: "longtask" });
    } catch {
      // longtask observer is Chromium-only; frame deltas still tell the story
    }
    let last = performance.now();
    let running = true;
    const tick = (t) => {
      deltas.push(t - last);
      last = t;
      if (running) requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
    const H = document.documentElement.scrollHeight - innerHeight;
    const start = performance.now();
    const dur = 5000;
    await new Promise((res) => {
      const step = () => {
        const p = (performance.now() - start) / dur;
        if (p >= 1) {
          running = false;
          return res();
        }
        const tri = p < 0.5 ? p * 2 : 2 - p * 2;
        scrollTo(0, Math.round(H * tri));
        requestAnimationFrame(step);
      };
      step();
    });
    deltas.shift();
    deltas.sort((a, b) => a - b);
    const q = (f) => deltas[Math.floor(f * (deltas.length - 1))];
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

import type { Page } from "@playwright/test";

// Deterministic capture is a two-track problem. page.clock fakes JS time
// (Date, performance.now, timers, requestAnimationFrame) but the CSS/
// compositor animation timeline keeps running on real time, so a frame is
// only reproducible when both tracks are pinned:
//   Track A (JS/canvas): clock installed at a fixed epoch and paused BEFORE
//     navigation, then advanced with runFor(seekMs) — every rAF tick the page
//     ever sees happens at the same mocked timestamps on every run. A seeded
//     Math.random override makes load-time randomness (particle layouts)
//     reproducible too.
//   Track B (CSS): after settle, every Animation object — keyframes,
//     @property-driven gradients, and pseudo-element animations — is paused
//     and seeked to the same currentTime via the Web Animations API.
// This works with zero cooperation from the page under capture.

export const CLOCK_EPOCH_MS = Date.UTC(2026, 0, 1);

// pauseAt must target a time ahead of the mocked "now"; the slack absorbs the
// few real milliseconds between install() and pauseAt() so the pre-navigation
// pause never races.
const PAUSE_SLACK_MS = 1_000;

export function seededRandomInitScript(seed: number): string {
  // mulberry32; self-contained because it runs as a page init script.
  return `(() => {
  let s = ${Math.trunc(seed)} >>> 0;
  Math.random = () => {
    s = (s + 0x6d2b79f5) | 0;
    let t = Math.imul(s ^ (s >>> 15), 1 | s);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
})();`;
}

export interface DeterminismOptions {
  seed?: number;
}

/** Call before page.goto: pins JS time and randomness for the next document. */
export async function installDeterminism(
  page: Page,
  options: DeterminismOptions = {},
): Promise<void> {
  await page.clock.install({ time: CLOCK_EPOCH_MS });
  await page.clock.pauseAt(CLOCK_EPOCH_MS + PAUSE_SLACK_MS);
  await page.addInitScript(seededRandomInitScript(options.seed ?? 1));
}

function freezeCssAnimationsAt(seekMs: number): number {
  let frozen = 0;
  for (const animation of document.getAnimations()) {
    try {
      animation.pause();
      animation.currentTime = seekMs;
      frozen += 1;
    } catch {
      // Some Animation states (idle transitions mid-cancel) reject seeking;
      // skipping them beats aborting the whole freeze.
    }
  }
  return frozen;
}

export interface SeekOptions {
  waitSelector?: string | undefined;
  settleMs?: number | undefined;
}

// Hydration and ResizeObserver deliveries run on real time and finish at a
// slightly different real moment every run. Because the mocked clock is
// paused, letting them all land before the advance means zero rAF ticks have
// elapsed when they do — every run then advances from an identical state.
// Without this settle, a canvas that mounts mid-advance integrates a
// run-varying time window and captures drift apart.
const DEFAULT_SETTLE_MS = 500;

/**
 * Call after page.goto: settles fonts, layout, and hydration, jumps the
 * mocked clock to seekMs, then freezes every CSS animation at that same
 * instant. Returns the number of CSS animations frozen.
 *
 * The jump is one fastForward, not a per-frame advance: replaying a seek as
 * 16ms ticks forces a software-rendered canvas frame per tick (tens of
 * seconds each on a heavy scene), and runFor can spin forever on zero-delay
 * re-arming timer chains. The trade: rAF-driven scenes receive a single
 * clamped tick — deterministic, but not the pose full playback would reach —
 * while every CSS animation is seeked to exactly seekMs by the WAAPI freeze.
 */
export async function seekTo(
  page: Page,
  seekMs: number,
  options: SeekOptions = {},
): Promise<number> {
  await page.evaluate(() => document.fonts.ready.then(() => undefined));
  if (options.waitSelector) {
    await page.waitForSelector(options.waitSelector, { state: "visible" });
  }
  // Driver-side sleep: page.waitForTimeout would go through the page's
  // (currently paused) mocked timers.
  await new Promise((resolve) => setTimeout(resolve, options.settleMs ?? DEFAULT_SETTLE_MS));
  if (seekMs > 0) {
    await page.clock.fastForward(seekMs);
  }
  return await page.evaluate(freezeCssAnimationsAt, seekMs);
}

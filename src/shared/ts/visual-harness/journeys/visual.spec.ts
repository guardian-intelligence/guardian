import { expect, test } from "@playwright/test";
import { loadCanaryConfig } from "../src/config.ts";
import { installDeterminism, seekTo } from "../src/determinism.ts";
import { attachFinding } from "../src/findings.ts";
import { foldFailures } from "../src/fold-probe.ts";
import { checkFold } from "../src/fold.ts";

const cfg = loadCanaryConfig(process.env);

for (const ff of cfg.formFactors) {
  test.describe(ff.name, () => {
    test.use({
      viewport: { width: ff.width, height: ff.height },
      deviceScaleFactor: ff.deviceScaleFactor,
      ...(ff.userAgent ? { userAgent: ff.userAgent } : {}),
      ...(ff.hasTouch ? { hasTouch: true } : {}),
      ...(ff.isMobile ? { isMobile: true } : {}),
      // reduce pins every prefers-reduced-motion style to its settled state,
      // which combined with the determinism seams makes baselines trivially
      // stable frames.
      contextOptions: { reducedMotion: "reduce" },
    });

    test(`${cfg.target.name} fold + drift`, async ({ page }, testInfo) => {
      test.setTimeout(cfg.timeoutMs);
      await installDeterminism(page, { seed: 1 });
      await page.goto(cfg.targetUrl, { waitUntil: "load" });
      await seekTo(page, cfg.seekMs, { waitSelector: cfg.target.waitSelector });

      const results = await checkFold(
        page,
        cfg.target.criticalSelectors,
        cfg.target.foldTolerancePx,
      );
      const dropped = foldFailures(results);
      for (const f of dropped) {
        await attachFinding(testInfo, {
          kind: "fold-drop",
          severity: "critical",
          target: cfg.target.name,
          formFactor: ff.name,
          selector: f.selector,
          status: f.status,
          clippedPx: f.clippedPx,
        });
      }
      // soft: a fold drop must not mask the drift signal (or vice versa) —
      // one run reports both findings.
      expect.soft(dropped, "critical elements must render above the fold").toEqual([]);

      try {
        await expect(page).toHaveScreenshot(`${cfg.target.name}__${ff.name}.png`);
      } catch (error) {
        await attachFinding(testInfo, {
          kind: "visual-drift",
          severity: "critical",
          target: cfg.target.name,
          formFactor: ff.name,
          engine: testInfo.project.name,
          message: error instanceof Error ? error.message.slice(0, 500) : String(error),
        });
        throw error;
      }
    });
  });
}

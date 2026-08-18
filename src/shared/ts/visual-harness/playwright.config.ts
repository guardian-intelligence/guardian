import { defineConfig } from "@playwright/test";

// The canary pod runs with a read-only root filesystem; /tmp is the only
// writable mount.
const outputDir = process.env.VISUAL_OUTPUT_DIR ?? "/tmp/visual-harness-output";

export default defineConfig({
  testDir: "./journeys",
  outputDir,
  // {platform} keeps macOS-rendered snapshots from ever colliding with the
  // linux baselines the canary compares against: font antialiasing differs
  // per OS, so baselines are only ever generated inside the pinned
  // @playwright_linux_amd64 image (see README).
  snapshotPathTemplate: "{testDir}/{testFileName}-snapshots/{arg}--{projectName}-{platform}{ext}",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: true,
  reporter: "./src/reporter.ts",
  expect: {
    toHaveScreenshot: {
      // Captures are byte-deterministic (seeded clock + WAAPI freeze), so the
      // comparison can run far stricter than Playwright's defaults — at the
      // default threshold 0.2, a two-channel ~40/255 hue shift on the hero
      // title blends under the per-pixel YIQ tolerance and sails through.
      maxDiffPixelRatio: 0.001,
      threshold: 0.05,
      // "disabled" would cancel infinite animations back to their initial
      // state, discarding the deterministic mid-animation freeze that
      // src/determinism.ts already applied; "allow" trusts that freeze.
      animations: "allow",
    },
  },
  use: {
    trace: "off",
    video: "off",
    screenshot: "off",
    // Bounded so a hung navigation or action names itself instead of riding
    // the whole test budget into a bare timeout.
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
    launchOptions: {
      // dev-shm/gpu flags are proven against this cluster's pod environment
      // (64Mi /dev/shm, no GPU device); srgb pins the color profile so
      // captures compare byte-for-byte across hosts.
      args: ["--disable-dev-shm-usage", "--disable-gpu", "--force-color-profile=srgb"],
    },
  },
  projects: [{ name: "chromium", use: { browserName: "chromium" } }],
});

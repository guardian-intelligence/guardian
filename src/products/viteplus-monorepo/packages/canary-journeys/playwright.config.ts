import { defineConfig } from "@playwright/test";

// Canaries handle credentials adjacent to management infrastructure
// (docs/canaries.md): traces, video, and screenshots capture typed secrets,
// so every capture stays off and failures surface as classified page states
// through the redacting reporter instead of pixels.
const outputDir = process.env.CANARY_OUTPUT_DIR ?? "/tmp/canary-journeys-output";

export default defineConfig({
  testDir: "./journeys",
  // The canary pod runs with a read-only root filesystem; /tmp is the only
  // writable mount.
  outputDir,
  globalTeardown: "./src/sanitize-artifacts.ts",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: true,
  reporter: "./src/redacting-reporter.ts",
  use: {
    trace: "off",
    video: "off",
    screenshot: "off",
    // Bounded so a hung navigation or action names itself instead of riding
    // the whole test budget into a bare timeout.
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
    // Debugging aid, off by default. Any HAR that gets recorded passes
    // through the sanitizing egress gate in globalTeardown before anything
    // can ship it.
    ...(process.env.CANARY_CAPTURE_HAR === "1"
      ? { contextOptions: { recordHar: { path: `${outputDir}/journey.har` } } }
      : {}),
  },
});

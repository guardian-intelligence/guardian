import { defineConfig } from "@playwright/test";

// Canaries handle credentials adjacent to management infrastructure
// (docs/canaries.md): traces, video, and screenshots capture typed secrets,
// so every capture stays off and failures surface as classified page states
// through the redacting reporter instead of pixels.
export default defineConfig({
  testDir: "./journeys",
  // The canary pod runs with a read-only root filesystem; /tmp is the only
  // writable mount.
  outputDir: "/tmp/canary-journeys-output",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: true,
  reporter: "./src/redacting-reporter.ts",
  use: {
    trace: "off",
    video: "off",
    screenshot: "off",
  },
});

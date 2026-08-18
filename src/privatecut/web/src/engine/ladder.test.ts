import { describe, expect, it } from "vitest";
import { planFrame } from "./ladder";

describe("planFrame", () => {
  it("keeps a 1080p30 source at 1080p when the budget is rich", () => {
    const plan = planFrame({
      sourceWidth: 1920,
      sourceHeight: 1080,
      sourceFrameRate: 30,
      videoBitsPerSecond: 6_000_000,
      codec: "avc",
    });
    expect(plan.height).toBe(1080);
    expect(plan.frameRate).toBe(30);
    expect(plan.isSource).toBe(true);
  });

  it("steps a 1080p source down to 720p on a 30s budget", () => {
    const plan = planFrame({
      sourceWidth: 1920,
      sourceHeight: 1080,
      sourceFrameRate: 30,
      videoBitsPerSecond: 950_000,
      codec: "avc",
    });
    expect(plan.height).toBe(720);
    expect(plan.width).toBe(1280);
  });

  it("halves 60fps before dropping below 720p", () => {
    const plan = planFrame({
      sourceWidth: 1920,
      sourceHeight: 1080,
      sourceFrameRate: 60,
      videoBitsPerSecond: 2_000_000,
      codec: "avc",
    });
    expect(plan.frameRate).toBe(30);
    expect(plan.height).toBeGreaterThanOrEqual(720);
  });

  it("preserves portrait aspect", () => {
    const plan = planFrame({
      sourceWidth: 1080,
      sourceHeight: 1920,
      sourceFrameRate: 30,
      videoBitsPerSecond: 2_500_000,
      codec: "avc",
    });
    expect(plan.height).toBeGreaterThan(plan.width);
    expect(plan.width % 2).toBe(0);
    expect(plan.height % 2).toBe(0);
  });

  it("falls back to the smallest rung when nothing meets the floor", () => {
    const plan = planFrame({
      sourceWidth: 3840,
      sourceHeight: 2160,
      sourceFrameRate: 60,
      videoBitsPerSecond: 200_000,
      codec: "avc",
    });
    expect(plan.height).toBeLessThanOrEqual(360 * (2160 / 3840) * 2);
  });

  it("never upscales a small source", () => {
    const plan = planFrame({
      sourceWidth: 640,
      sourceHeight: 360,
      sourceFrameRate: 30,
      videoBitsPerSecond: 10_000_000,
      codec: "avc",
    });
    expect(plan.width).toBe(640);
    expect(plan.height).toBe(360);
    expect(plan.isSource).toBe(true);
  });
});

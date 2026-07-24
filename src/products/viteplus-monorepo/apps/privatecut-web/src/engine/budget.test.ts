import { describe, expect, it } from "vitest";
import { estimateContainerBytes, planAudio, planBudget } from "./budget";
import { SIZE_LIMIT_BYTES } from "./limits";

describe("planAudio", () => {
  it("gives short clips full stereo AAC", () => {
    expect(planAudio(5, SIZE_LIMIT_BYTES, true)).toEqual({ bitrate: 128_000, numberOfChannels: 2 });
  });

  it("steps down the ladder as duration grows", () => {
    expect(planAudio(60, SIZE_LIMIT_BYTES, true)).toEqual({ bitrate: 64_000, numberOfChannels: 2 });
  });

  it("drops to mono only when the budget is tight", () => {
    const plan = planAudio(120, SIZE_LIMIT_BYTES, true);
    expect(plan).toEqual({ bitrate: 48_000, numberOfChannels: 1 });
  });

  it("returns null without a source audio track", () => {
    expect(planAudio(10, SIZE_LIMIT_BYTES, false)).toBeNull();
  });
});

describe("planBudget", () => {
  it("hands video everything the container and audio do not take", () => {
    const plan = planBudget({ durationS: 15, frameRate: 30, sourceHasAudio: true });
    const expectedVideoBytes = SIZE_LIMIT_BYTES - plan.containerBytes - plan.audioBytes;
    expect(plan.videoBitsPerSecond).toBeCloseTo((expectedVideoBytes * 8) / 15, 5);
    // 15s with audio lands near the 2 Mbps reference point.
    expect(plan.videoBitsPerSecond).toBeGreaterThan(1_800_000);
    expect(plan.videoBitsPerSecond).toBeLessThan(2_200_000);
  });

  it("never spends more bits than the source carries", () => {
    const plan = planBudget({
      durationS: 5,
      frameRate: 30,
      sourceHasAudio: false,
      sourceVideoBitsPerSecond: 900_000,
    });
    expect(plan.videoBitsPerSecond).toBe(900_000);
  });

  it("scales container overhead with frame count", () => {
    const short = estimateContainerBytes(5, 30, true);
    const long = estimateContainerBytes(60, 60, true);
    expect(long).toBeGreaterThan(short * 5);
  });
});

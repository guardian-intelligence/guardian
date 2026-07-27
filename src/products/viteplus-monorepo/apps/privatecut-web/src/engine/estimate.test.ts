import { describe, expect, it } from "vitest";
import type { OutputProfile, ProbeSummary, SelectionRange } from "./types";
import { estimateSelection } from "./estimate";

const MP4_PROFILE: OutputProfile = {
  format: "mp4",
  videoCodec: "avc",
  audioCodec: "aac",
};

const SUMMARY: ProbeSummary = {
  durationS: 180,
  container: "webm",
  video: {
    width: 1920,
    height: 1080,
    frameRate: 30,
    codec: "vp8",
    bitsPerSecond: 8_000_000,
  },
  hasAudio: true,
  audioCodec: "opus",
  outputProfiles: [MP4_PROFILE],
  keyframesS: [0],
};

const THREE_MINUTES: SelectionRange = { startS: 0, endS: 180 };

describe("estimateSelection quality warning", () => {
  it("warns when a three-minute clip is forced below the quality floor", () => {
    const estimate = estimateSelection(SUMMARY, THREE_MINUTES, MP4_PROFILE, 4_000_000);

    expect(estimate.label).toBe("360p");
    expect(estimate.lowQuality).toBe(true);
  });

  it("reacts to a larger size cap", () => {
    const estimate = estimateSelection(SUMMARY, THREE_MINUTES, MP4_PROFILE, 100_000_000);

    expect(estimate.label).toBe("original size");
    expect(estimate.lowQuality).toBe(false);
  });

  it("reacts to a shorter selection", () => {
    const estimate = estimateSelection(SUMMARY, { startS: 0, endS: 30 }, MP4_PROFILE, 4_000_000);

    expect(estimate.label).not.toBe("360p");
    expect(estimate.lowQuality).toBe(false);
  });

  it("does not warn when a low-resolution source is preserved at a sufficient bitrate", () => {
    const estimate = estimateSelection(
      {
        ...SUMMARY,
        video: {
          ...SUMMARY.video,
          width: 640,
          height: 360,
          bitsPerSecond: 250_000,
        },
      },
      THREE_MINUTES,
      MP4_PROFILE,
      10_000_000,
    );

    expect(estimate.lowQuality).toBe(false);
  });
});

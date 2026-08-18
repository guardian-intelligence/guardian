import { describe, expect, it } from "vitest";
import type { OutputProfile } from "./types";
import { recorderMimeCandidates, selectRecorderMimeType } from "./native-media";

const MP4: OutputProfile = {
  format: "mp4",
  videoCodec: "avc",
  audioCodec: "aac",
};

const WEBM: OutputProfile = {
  format: "webm",
  videoCodec: "vp9",
  audioCodec: "opus",
};

describe("native media recorder profiles", () => {
  it("requests H.264 and AAC when recording MP4", () => {
    expect(recorderMimeCandidates(MP4, true)).toEqual([
      "video/mp4;codecs=avc1.42001f,mp4a.40.2",
      "video/mp4;codecs=avc1,mp4a.40.2",
    ]);
  });

  it("requests the resolved WebM codecs", () => {
    expect(recorderMimeCandidates(WEBM, true)).toEqual(["video/webm;codecs=vp9,opus"]);
  });

  it("chooses the first profile the browser can record", () => {
    expect(selectRecorderMimeType(MP4, true, (mimeType) => !mimeType.includes("42001f"))).toBe(
      "video/mp4;codecs=avc1,mp4a.40.2",
    );
  });

  it("refuses to fall back to an unspecified container codec", () => {
    expect(selectRecorderMimeType(WEBM, true, () => false)).toBeNull();
  });
});

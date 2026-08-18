import { describe, expect, it } from "vitest";
import {
  canRemuxCodecs,
  INPUT_CONTAINER_LABEL,
  INPUT_FILE_ACCEPT,
  OUTPUT_CONTAINER_INFO,
  OUTPUT_CONTAINERS,
} from "./formats";

describe("input formats", () => {
  it("advertises every video-bearing container accepted by the dropzone", () => {
    expect(INPUT_CONTAINER_LABEL).toBe("MP4, M4V, MOV, MKV, WebM, Ogg, or MPEG-TS");
    for (const extension of [".mp4", ".m4v", ".mov", ".mkv", ".webm", ".ogg", ".ts"]) {
      expect(INPUT_FILE_ACCEPT.split(",")).toContain(extension);
    }
  });

  it("does not advertise unsupported AVI or AVIF inputs", () => {
    expect(INPUT_FILE_ACCEPT).not.toContain(".avi");
    expect(INPUT_FILE_ACCEPT).not.toContain(".avif");
  });
});

describe("output formats", () => {
  it("exposes MP4 and WebM with matching MIME types and extensions", () => {
    expect(OUTPUT_CONTAINERS).toEqual(["mp4", "webm"]);
    expect(OUTPUT_CONTAINER_INFO.mp4).toMatchObject({
      extension: ".mp4",
      mimeType: "video/mp4",
    });
    expect(OUTPUT_CONTAINER_INFO.webm).toMatchObject({
      extension: ".webm",
      mimeType: "video/webm",
    });
  });

  it("remuxes native MP4 video codecs and WebM codec combinations", () => {
    expect(canRemuxCodecs("mp4", "avc", "aac")).toBe(true);
    expect(canRemuxCodecs("mp4", "hevc", "aac")).toBe(true);
    expect(canRemuxCodecs("mp4", "vp9", "opus")).toBe(false);
    expect(canRemuxCodecs("webm", "vp9", "opus")).toBe(true);
    expect(canRemuxCodecs("webm", "av1", null)).toBe(true);
    expect(canRemuxCodecs("webm", "avc", "aac")).toBe(false);
  });
});

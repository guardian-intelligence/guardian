import { describe, expect, it } from "vitest";
import { resolveVideoDecodeMode } from "./video-decode";

describe("video decoder routing", () => {
  it("keeps browser-decodable inputs in the worker", () => {
    expect(resolveVideoDecodeMode(true, "avc", true)).toBe("webcodecs");
  });

  it("routes local HEVC files through the native media element", () => {
    expect(resolveVideoDecodeMode(false, "hevc", true)).toBe("media-element");
  });

  it("does not route remote or unrelated unsupported codecs through the native path", () => {
    expect(resolveVideoDecodeMode(false, "hevc", false)).toBeNull();
    expect(resolveVideoDecodeMode(false, "av1", true)).toBeNull();
  });
});

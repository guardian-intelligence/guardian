import { describe, expect, it } from "vitest";
import { parseStatusId, pickVariant, syndicationToken, toVariant } from "./x-link";

describe("parseStatusId", () => {
  it.each([
    ["https://x.com/NBA/status/1936903309425619013", "1936903309425619013"],
    ["https://twitter.com/NBA/status/1936903309425619013", "1936903309425619013"],
    ["https://mobile.twitter.com/NBA/status/1936903309425619013", "1936903309425619013"],
    ["https://www.x.com/NBA/status/1936903309425619013", "1936903309425619013"],
    ["x.com/NBA/status/1936903309425619013", "1936903309425619013"],
    ["https://x.com/i/status/1936903309425619013", "1936903309425619013"],
    ["https://x.com/i/web/status/1936903309425619013", "1936903309425619013"],
    ["https://x.com/NBA/statuses/1936903309425619013", "1936903309425619013"],
    ["https://x.com/NBA/status/1936903309425619013/video/1", "1936903309425619013"],
    ["https://x.com/NBA/status/1936903309425619013?s=46&t=abc", "1936903309425619013"],
    ["  https://x.com/NBA/status/1936903309425619013  ", "1936903309425619013"],
  ])("accepts %s", (input, id) => {
    expect(parseStatusId(input)).toBe(id);
  });

  it.each([
    "",
    "not a url",
    "https://example.com/NBA/status/1936903309425619013",
    "https://x.com.evil.io/NBA/status/1936903309425619013",
    "https://x.com/NBA/status/notanumber",
    "https://x.com/NBA",
    "https://x.com/home",
    "https://t.co/7doAmnD9mI",
  ])("rejects %s", (input) => {
    expect(parseStatusId(input)).toBeNull();
  });
});

describe("syndicationToken", () => {
  // Captured live from working requests on 2026-07-22; the derivation must
  // keep producing exactly these or the syndication endpoint 404s.
  it("matches known-good tokens", () => {
    expect(syndicationToken("20")).toBe("6dq1a2xwd93");
    expect(syndicationToken("1936903309425619013")).toBe("4pylq3o8ep");
  });
});

describe("toVariant", () => {
  it("keeps mp4 variants on video.twimg.com and parses dimensions", () => {
    expect(
      toVariant({
        content_type: "video/mp4",
        bitrate: 2_176_000,
        url: "https://video.twimg.com/amplify_video/1/vid/avc1/1280x720/x.mp4?tag=16",
      }),
    ).toEqual({
      url: "https://video.twimg.com/amplify_video/1/vid/avc1/1280x720/x.mp4?tag=16",
      bitrate: 2_176_000,
      width: 1280,
      height: 720,
    });
  });

  it("drops m3u8 playlists", () => {
    expect(
      toVariant({
        content_type: "application/x-mpegURL",
        url: "https://video.twimg.com/amplify_video/1/pl/x.m3u8",
      }),
    ).toBeNull();
  });

  it("drops mp4 urls not on video.twimg.com", () => {
    expect(
      toVariant({ content_type: "video/mp4", bitrate: 1, url: "https://evil.io/x.mp4" }),
    ).toBeNull();
  });
});

describe("pickVariant", () => {
  const ladder = [
    { url: "a", bitrate: 288_000, width: 480, height: 270 },
    { url: "b", bitrate: 10_368_000, width: 1920, height: 1080 },
    { url: "c", bitrate: 2_176_000, width: 1280, height: 720 },
  ];

  it("takes the highest bitrate", () => {
    expect(pickVariant(ladder)?.url).toBe("b");
  });

  it("returns null for an empty ladder", () => {
    expect(pickVariant([])).toBeNull();
  });
});

import { describe, expect, it } from "vitest";
import { loadCanaryConfig, parseGoDuration } from "../src/config.ts";
import { FORM_FACTORS } from "../src/form-factors.ts";

const baseEnv = { VISUAL_TARGET_URL: "https://rumi.engineering/" };

describe("parseGoDuration", () => {
  it("parses minutes and seconds", () => {
    expect(parseGoDuration("2m30s")).toBe(150_000);
    expect(parseGoDuration("45s")).toBe(45_000);
    expect(parseGoDuration("3m")).toBe(180_000);
  });

  it("rejects malformed specs", () => {
    for (const bad of ["", "5", "1h", "s30", "2m30"]) {
      expect(() => parseGoDuration(bad)).toThrow(/invalid duration/);
    }
  });
});

describe("loadCanaryConfig", () => {
  it("applies defaults", () => {
    const cfg = loadCanaryConfig(baseEnv);
    expect(cfg.target.name).toBe("shortty");
    expect(cfg.formFactors).toEqual(FORM_FACTORS);
    expect(cfg.seekMs).toBe(3_600);
    expect(cfg.timeoutMs).toBe(120_000);
  });

  it("requires a target URL", () => {
    expect(() => loadCanaryConfig({})).toThrow(/absolute URL/);
  });

  it("rejects plain HTTP for non-loopback hosts", () => {
    expect(() => loadCanaryConfig({ VISUAL_TARGET_URL: "http://rumi.engineering/" })).toThrow(
      /HTTPS/,
    );
  });

  it("allows plain HTTP for loopback", () => {
    expect(loadCanaryConfig({ VISUAL_TARGET_URL: "http://127.0.0.1:4253" }).targetUrl).toBe(
      "http://127.0.0.1:4253",
    );
  });

  it("allows plain HTTP when explicitly opted in", () => {
    const env = { VISUAL_TARGET_URL: "http://shortty:8080", VISUAL_ALLOW_HTTP: "1" };
    expect(loadCanaryConfig(env).targetUrl).toBe("http://shortty:8080");
  });

  it("bounds the timeout", () => {
    expect(() => loadCanaryConfig({ ...baseEnv, VISUAL_TIMEOUT: "30s" })).toThrow(/between/);
    expect(() => loadCanaryConfig({ ...baseEnv, VISUAL_TIMEOUT: "10m" })).toThrow(/between/);
  });

  it("bounds the seek", () => {
    expect(() => loadCanaryConfig({ ...baseEnv, VISUAL_SEEK_MS: "-1" })).toThrow(/VISUAL_SEEK_MS/);
    expect(() => loadCanaryConfig({ ...baseEnv, VISUAL_SEEK_MS: "90000" })).toThrow(
      /VISUAL_SEEK_MS/,
    );
    expect(loadCanaryConfig({ ...baseEnv, VISUAL_SEEK_MS: "1500" }).seekMs).toBe(1_500);
  });

  it("selects form factors from a comma list", () => {
    const cfg = loadCanaryConfig({ ...baseEnv, VISUAL_FORM_FACTORS: "mobile, laptop" });
    expect(cfg.formFactors.map((f) => f.name)).toEqual(["mobile", "laptop"]);
  });

  it("rejects unknown targets", () => {
    expect(() => loadCanaryConfig({ ...baseEnv, VISUAL_TARGET: "nope" })).toThrow(/unknown target/);
  });
});

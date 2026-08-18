import { describe, expect, it } from "vitest";
import { loadJourneyConfig, parseGoDuration } from "../src/config.ts";

const CREDS = {
  GITHUB_CANARY_USERNAME: "guardian-login-canary-01",
  GITHUB_CANARY_PASSWORD: "example-password",
  GITHUB_CANARY_TOTP_SECRET: "GEZDGNBVGY3TQOJQ", // gitleaks:allow -- RFC 6238 public test vector
};

describe("parseGoDuration", () => {
  it("parses minute and second spans", () => {
    expect(parseGoDuration("2m30s")).toBe(150_000);
    expect(parseGoDuration("90s")).toBe(90_000);
    expect(parseGoDuration("3m")).toBe(180_000);
  });

  it("rejects malformed spans", () => {
    expect(() => parseGoDuration("")).toThrow(/invalid duration/);
    expect(() => parseGoDuration("2h")).toThrow(/invalid duration/);
  });
});

describe("loadJourneyConfig", () => {
  it("applies defaults and derives the Guardian host", () => {
    const cfg = loadJourneyConfig({ ...CREDS });
    expect(cfg.pageUrl).toBe("https://guardianintelligence.org/postflight");
    expect(cfg.guardianHost).toBe("guardianintelligence.org");
    expect(cfg.timeoutMs).toBe(150_000);
  });

  it("bounds the timeout between one and five minutes", () => {
    expect(() => loadJourneyConfig({ ...CREDS, CANARY_TIMEOUT: "30s" })).toThrow(
      /between 1m and 5m/,
    );
    expect(() => loadJourneyConfig({ ...CREDS, CANARY_TIMEOUT: "6m" })).toThrow(
      /between 1m and 5m/,
    );
  });

  it("requires an absolute HTTPS page URL", () => {
    expect(() => loadJourneyConfig({ ...CREDS, POSTFLIGHT_URL: "http://example.com" })).toThrow(
      /absolute HTTPS URL/,
    );
    expect(() => loadJourneyConfig({ ...CREDS, POSTFLIGHT_URL: "not a url" })).toThrow(
      /absolute HTTPS URL/,
    );
  });

  it("requires the full credential trio", () => {
    expect(() => loadJourneyConfig({ ...CREDS, GITHUB_CANARY_PASSWORD: undefined })).toThrow(
      /username, password, and TOTP secret/,
    );
  });
});

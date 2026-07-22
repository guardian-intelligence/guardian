import { describe, expect, it } from "vitest";
import { decodeBase32, totp, totpBoundaryDelayMs } from "../src/totp.ts";

describe("totp", () => {
  it("matches the RFC 6238 SHA-1 vector", () => {
    expect(totp("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", new Date(59_000))).toBe("287082");
  });

  it("normalizes spacing and casing in seeds", () => {
    expect(totp("gezd gnbv gy3t qojq gezd gnbv gy3t qojq", new Date(59_000))).toBe("287082");
  });

  it("rejects seeds that are not base32", () => {
    expect(() => totp("not-a-seed!", new Date(59_000))).toThrow(/base32/);
  });

  it("rejects seeds that are too short", () => {
    expect(() => totp("GEZDGNBV", new Date(59_000))).toThrow(/invalid/);
  });

  it("decodes base32 without padding", () => {
    expect(Buffer.from(decodeBase32("GEZDGNBVGY3TQOJQ")).toString()).toBe("1234567890");
  });
});

describe("totpBoundaryDelayMs", () => {
  it("returns zero away from a window boundary", () => {
    expect(totpBoundaryDelayMs(new Date(20_000))).toBe(0);
  });

  it("waits out the window tail near a boundary", () => {
    expect(totpBoundaryDelayMs(new Date(29_000))).toBe(2_000);
  });
});

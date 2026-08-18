import { describe, expect, it } from "vitest";
import { browserRandom32 } from "../src/random.ts";

describe("browserRandom32", () => {
  it("draws the full 32 bits from the platform CSPRNG", () => {
    const draws = Array.from({ length: 64 }, () => browserRandom32());
    expect(draws.every((n) => Number.isInteger(n) && n >= 0 && n <= 0xffff_ffff)).toBe(true);
    // A truncated or low-entropy draw would collide across 64 samples.
    expect(new Set(draws).size).toBe(draws.length);
    // Values must reach above 2^31 — a signed-read bug would cap them.
    expect(Math.max(...draws)).toBeGreaterThan(0x4000_0000);
  });
});

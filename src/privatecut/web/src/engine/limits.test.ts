import { describe, expect, it } from "vitest";
import {
  DEFAULT_SIZE_LIMIT_BYTES,
  isSizeLimit,
  MAX_SELECTION_SECONDS,
  SIZE_LIMIT_PRESETS_BYTES,
} from "./limits";

describe("isSizeLimit", () => {
  it("accepts every preset", () => {
    for (const preset of SIZE_LIMIT_PRESETS_BYTES) {
      expect(isSizeLimit(preset)).toBe(true);
    }
  });

  it("rejects anything off the preset list", () => {
    expect(isSizeLimit(0)).toBe(false);
    expect(isSizeLimit(4_000_001)).toBe(false);
    expect(isSizeLimit(-4_000_000)).toBe(false);
    expect(isSizeLimit(Number.NaN)).toBe(false);
  });

  it("keeps the default on the preset list", () => {
    expect(isSizeLimit(DEFAULT_SIZE_LIMIT_BYTES)).toBe(true);
  });
});

describe("selection limit", () => {
  it("allows clips up to three minutes", () => {
    expect(MAX_SELECTION_SECONDS).toBe(180);
  });
});

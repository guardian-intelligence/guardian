import { describe, expect, it } from "vitest";
import { converge, firstPassBitrate, repricedBitrate } from "./convergence";
import { DEFAULT_SAFETY_MARGIN, MAX_PASSES, SIZE_LIMIT_BYTES } from "./limits";

// A fake encoder with a hidden linear rate model: bytes = overhead + bits×k/8.
// `bias` models an encoder that systematically over- or under-shoots the
// requested bitrate, which is exactly what real encoders do.
function fakeEncoder(overheadBytes: number, durationS: number, bias: number) {
  const calls: number[] = [];
  return {
    calls,
    encode: (videoBitsPerSecond: number) => {
      calls.push(videoBitsPerSecond);
      const bytes = Math.round(overheadBytes + (videoBitsPerSecond * bias * durationS) / 8);
      return Promise.resolve({ bytes, artifact: { bytes } });
    },
  };
}

describe("firstPassBitrate", () => {
  it("applies the safety margin", () => {
    expect(firstPassBitrate({ initialVideoBitsPerSecond: 1_000_000 })).toBe(
      Math.floor(1_000_000 * DEFAULT_SAFETY_MARGIN),
    );
  });

  it("respects the source-bitrate ceiling", () => {
    expect(
      firstPassBitrate({ initialVideoBitsPerSecond: 1_000_000, maxVideoBitsPerSecond: 500_000 }),
    ).toBe(500_000);
  });
});

describe("converge", () => {
  it("accepts a well-behaved encoder on the first pass", async () => {
    const reserved = 120_000;
    const duration = 15;
    const encoder = fakeEncoder(reserved, duration, 1.0);
    const initial = ((SIZE_LIMIT_BYTES - reserved) * 8) / duration;
    const result = await converge({
      limitBytes: SIZE_LIMIT_BYTES,
      reservedBytes: reserved,
      initialVideoBitsPerSecond: initial,
      encode: encoder.encode,
    });
    expect(result.passes).toHaveLength(1);
    expect(result.bytes).toBeLessThanOrEqual(SIZE_LIMIT_BYTES);
    expect(result.utilization).toBeGreaterThan(0.88);
  });

  it("re-encodes when the encoder overshoots, and never accepts an oversized file", async () => {
    const reserved = 120_000;
    const duration = 30;
    // 15% overshoot: the first (margined) pass lands over the limit.
    const encoder = fakeEncoder(reserved, duration, 1.15);
    const initial = ((SIZE_LIMIT_BYTES - reserved) * 8) / duration;
    const result = await converge({
      limitBytes: SIZE_LIMIT_BYTES,
      reservedBytes: reserved,
      initialVideoBitsPerSecond: initial,
      safetyMargin: 0.99,
      encode: encoder.encode,
    });
    expect(result.passes.length).toBeGreaterThan(1);
    expect(result.bytes).toBeLessThanOrEqual(SIZE_LIMIT_BYTES);
    expect(result.utilization).toBeGreaterThan(0.88);
  });

  it("converts a deep undershoot into another pass and better utilization", async () => {
    const reserved = 60_000;
    const duration = 10;
    // Encoder produces 40% fewer bits than asked.
    const encoder = fakeEncoder(reserved, duration, 0.6);
    const initial = ((SIZE_LIMIT_BYTES - reserved) * 8) / duration;
    const result = await converge({
      limitBytes: SIZE_LIMIT_BYTES,
      reservedBytes: reserved,
      initialVideoBitsPerSecond: initial,
      encode: encoder.encode,
    });
    expect(result.bytes).toBeLessThanOrEqual(SIZE_LIMIT_BYTES);
    expect(result.utilization).toBeGreaterThan(0.88);
  });

  it("keeps the best under-limit artifact when passes are exhausted", async () => {
    const reserved = 100_000;
    const duration = 20;
    let call = 0;
    // Erratic encoder: alternates deep undershoot and slight overshoot.
    const encode = (videoBitsPerSecond: number) => {
      call += 1;
      const bias = call % 2 === 1 ? 0.5 : 1.02;
      const bytes = Math.round(reserved + (videoBitsPerSecond * bias * duration) / 8);
      return Promise.resolve({ bytes, artifact: { bytes } });
    };
    const initial = ((SIZE_LIMIT_BYTES - reserved) * 8) / duration;
    const result = await converge({
      limitBytes: SIZE_LIMIT_BYTES,
      reservedBytes: reserved,
      initialVideoBitsPerSecond: initial,
      encode,
    });
    expect(result.passes.length).toBeLessThanOrEqual(MAX_PASSES);
    expect(result.bytes).toBeLessThanOrEqual(SIZE_LIMIT_BYTES);
  });

  it("throws rather than accept when every pass lands over the limit", async () => {
    const encode = () => Promise.resolve({ bytes: SIZE_LIMIT_BYTES + 1, artifact: { bytes: 0 } });
    await expect(
      converge({
        limitBytes: SIZE_LIMIT_BYTES,
        reservedBytes: 0,
        initialVideoBitsPerSecond: 1_000_000,
        encode,
      }),
    ).rejects.toThrow(/under the size limit/);
  });

  it("stops early at the source-bitrate ceiling instead of chasing utilization", async () => {
    const reserved = 50_000;
    const duration = 5;
    const ceiling = 800_000;
    const encoder = fakeEncoder(reserved, duration, 1.0);
    const result = await converge({
      limitBytes: SIZE_LIMIT_BYTES,
      reservedBytes: reserved,
      initialVideoBitsPerSecond: ceiling,
      maxVideoBitsPerSecond: ceiling,
      encode: encoder.encode,
    });
    // A tiny source can't fill 4MB in 5s; the loop must not burn passes.
    expect(result.passes.length).toBeLessThanOrEqual(2);
    expect(result.bytes).toBeLessThanOrEqual(SIZE_LIMIT_BYTES);
  });
});

describe("repricedBitrate", () => {
  it("scales down after an overshoot", () => {
    const next = repricedBitrate(
      { videoBitsPerSecond: 2_000_000, bytes: SIZE_LIMIT_BYTES + 200_000 },
      SIZE_LIMIT_BYTES,
      100_000,
    );
    expect(next).toBeLessThan(2_000_000);
  });

  it("scales up after an undershoot, capped by the ceiling", () => {
    const next = repricedBitrate(
      { videoBitsPerSecond: 1_000_000, bytes: 2_000_000 },
      SIZE_LIMIT_BYTES,
      100_000,
      1_400_000,
    );
    expect(next).toBe(1_400_000);
  });
});

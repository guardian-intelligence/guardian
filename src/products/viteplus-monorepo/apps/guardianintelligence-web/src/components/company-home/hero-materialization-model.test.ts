import { describe, expect, it } from "vitest";
import {
  activationThreshold,
  deterministicNoise,
  materializationPixelOpacity,
  pixelLightState,
  spotlightEdge,
  type MaterializationPixel,
} from "./hero-materialization-model";

function pixel(normalizedX: number, normalizedY = 0): MaterializationPixel {
  return {
    activation: activationThreshold(12, 4, normalizedX, normalizedY),
    column: 12,
    normalizedX,
    normalizedY,
    row: 4,
    x: 0,
    y: 0,
  };
}

describe("hero materialization", () => {
  it("uses a deterministic activation field", () => {
    expect(deterministicNoise(12, 4)).toBe(deterministicNoise(12, 4));
    expect(deterministicNoise(12, 4)).not.toBe(deterministicNoise(13, 4));
  });

  it("biases activation toward the center", () => {
    const center = Array.from({ length: 40 }, (_, index) => activationThreshold(index, 3, 0.08, 0));
    const edge = Array.from({ length: 40 }, (_, index) => activationThreshold(index, 3, 0.92, 0));
    const average = (values: readonly number[]) =>
      values.reduce((total, value) => total + value, 0) / values.length;
    expect(average(center)).toBeLessThan(average(edge));
  });

  it("never turns an activated pixel off", () => {
    const sample = pixel(0.55, 0.2);
    const states = [0, 0.25, 0.5, 0.75, 1].map((progress) => pixelLightState(sample, progress));
    const firstLit = states.findIndex((state) => state !== "off");
    expect(firstLit).toBeGreaterThanOrEqual(0);
    expect(states.slice(firstLit)).not.toContain("off");
  });

  it("expands both spotlight fronts monotonically", () => {
    for (const side of [-1, 1] as const) {
      const edges = [0, 0.2, 0.4, 0.6, 0.8, 1].map((progress) =>
        spotlightEdge(0.37, progress, side),
      );
      for (let index = 1; index < edges.length; index += 1) {
        expect(edges[index]!).toBeGreaterThanOrEqual(edges[index - 1]!);
      }
    }
  });

  it("crossfades the pixel field into the final outline without a discontinuity", () => {
    const samples = [0.82, 0.86, 0.9, 0.94, 0.98, 1].map(materializationPixelOpacity);
    expect(samples[0]).toBe(1);
    expect(samples.at(-1)).toBe(0);
    for (let index = 1; index < samples.length; index += 1) {
      expect(samples[index]!).toBeLessThanOrEqual(samples[index - 1]!);
    }
  });
});

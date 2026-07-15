import { describe, expect, it } from "vitest";
import { median, metricMedians } from "./statistics.mjs";

describe("performance statistics", () => {
  it("returns the middle sorted value", () => {
    expect(median([24.6, 23.2, 22.8])).toBe(23.2);
  });

  it("calculates each metric median independently", () => {
    const medians = metricMedians(
      [
        { avg: 24.6, "jank>33ms": 16 },
        { avg: 23.2, "jank>33ms": 8 },
        { avg: 22.8, "jank>33ms": 18 },
      ],
      ["avg", "jank>33ms"],
    );

    expect(medians).toEqual({ avg: 23.2, "jank>33ms": 16 });
  });
});

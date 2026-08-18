import { describe, expect, it } from "vitest";
import { classifyFold, foldFailures, type FoldMeasurement } from "../src/fold-probe.ts";

const measurement = (overrides: Partial<FoldMeasurement>): FoldMeasurement => ({
  selector: ".hero",
  found: true,
  top: 100,
  bottom: 300,
  viewportHeight: 800,
  ...overrides,
});

describe("classifyFold", () => {
  it("classifies an element fully inside the viewport as above", () => {
    expect(classifyFold(measurement({}))).toEqual({
      selector: ".hero",
      status: "above",
      clippedPx: 0,
    });
  });

  it("classifies a missing element", () => {
    expect(classifyFold(measurement({ found: false }))).toEqual({
      selector: ".hero",
      status: "missing",
      clippedPx: 0,
    });
  });

  it("classifies an element starting past the viewport as below", () => {
    const result = classifyFold(measurement({ top: 900, bottom: 1100 }));
    expect(result.status).toBe("below");
    expect(result.clippedPx).toBe(200);
  });

  it("classifies an element straddling the fold as partial with clipped depth", () => {
    const result = classifyFold(measurement({ top: 700, bottom: 950 }));
    expect(result.status).toBe("partial");
    expect(result.clippedPx).toBe(150);
  });

  it("treats an element exactly at the fold boundary as below", () => {
    expect(classifyFold(measurement({ top: 800, bottom: 900 })).status).toBe("below");
  });

  it("absorbs sub-tolerance clipping as above", () => {
    expect(classifyFold(measurement({ top: 700, bottom: 806 }), 8).status).toBe("above");
  });

  it("still flags clipping beyond the tolerance", () => {
    expect(classifyFold(measurement({ top: 700, bottom: 810 }), 8).status).toBe("partial");
  });
});

describe("foldFailures", () => {
  it("keeps everything that is not fully above the fold", () => {
    const results = [
      classifyFold(measurement({})),
      classifyFold(measurement({ selector: ".cta", top: 900, bottom: 1000 })),
      classifyFold(measurement({ selector: ".gone", found: false })),
    ];
    expect(foldFailures(results).map((r) => r.selector)).toEqual([".cta", ".gone"]);
  });
});

import { describe, expect, it } from "vite-plus/test";
import { companyVisualIntegrityFailures } from "./company-visual-integrity";

const stable = {
  layoutShift: 0,
  longFrameCount: 0,
  longFrameTotalMs: 0,
  maxLongFrameMs: 0,
  motionExpected: true,
  prematureContent: false,
};

describe("company visual integrity", () => {
  it("accepts a stable intro", () => {
    expect(companyVisualIntegrityFailures(stable)).toEqual([]);
  });

  it("detects content painted before the intro starts", () => {
    expect(companyVisualIntegrityFailures({ ...stable, prematureContent: true })).toEqual([
      "premature-content",
    ]);
  });

  it("detects layout instability", () => {
    expect(companyVisualIntegrityFailures({ ...stable, layoutShift: 0.021 })).toEqual([
      "layout-shift",
    ]);
  });

  it("detects one severe or several cumulative long frames", () => {
    expect(companyVisualIntegrityFailures({ ...stable, maxLongFrameMs: 250 })).toEqual([
      "frame-thrash",
    ]);
    expect(
      companyVisualIntegrityFailures({
        ...stable,
        longFrameCount: 3,
        longFrameTotalMs: 300,
        maxLongFrameMs: 100,
      }),
    ).toEqual(["frame-thrash"]);
  });

  it("does not treat an intentionally static scene as a premature paint", () => {
    expect(
      companyVisualIntegrityFailures({
        ...stable,
        longFrameCount: 3,
        longFrameTotalMs: 300,
        maxLongFrameMs: 300,
        motionExpected: false,
        prematureContent: true,
      }),
    ).toEqual([]);
  });
});

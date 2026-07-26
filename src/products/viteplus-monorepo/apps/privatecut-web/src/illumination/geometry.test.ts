import { describe, expect, it } from "vitest";
import { rectangleAround, toWebGLScissor, unionRectangles } from "./geometry";

describe("illumination dirty rectangles", () => {
  it("clips a light footprint to the viewport", () => {
    expect(rectangleAround(10, 20, 40, 100, 80)).toEqual({
      height: 60,
      width: 50,
      x: 0,
      y: 0,
    });
  });

  it("unites the old and new light footprints", () => {
    expect(
      unionRectangles(
        { height: 40, width: 40, x: 10, y: 20 },
        { height: 50, width: 50, x: 30, y: 5 },
      ),
    ).toEqual({ height: 55, width: 70, x: 10, y: 5 });
  });

  it("converts top-left css pixels into bottom-left WebGL pixels", () => {
    expect(toWebGLScissor({ height: 20, width: 30, x: 10, y: 15 }, 100, 1.25)).toEqual({
      height: 26,
      width: 38,
      x: 12,
      y: 81,
    });
  });
});

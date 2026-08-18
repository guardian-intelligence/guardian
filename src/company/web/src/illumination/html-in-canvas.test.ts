import { describe, expect, it } from "vitest";
import { canRenderHtmlInCanvas } from "./html-in-canvas";

describe("html-in-canvas capability detection", () => {
  it("requires both element capture and explicit paint scheduling", () => {
    expect(
      canRenderHtmlInCanvas(
        { requestPaint: () => {} },
        { drawElementImage: () => ({}) as DOMMatrix },
      ),
    ).toBe(true);
    expect(canRenderHtmlInCanvas({}, { drawElementImage: () => ({}) as DOMMatrix })).toBe(false);
    expect(canRenderHtmlInCanvas({ requestPaint: () => {} }, {})).toBe(false);
    expect(canRenderHtmlInCanvas({ requestPaint: () => {} }, null)).toBe(false);
  });
});

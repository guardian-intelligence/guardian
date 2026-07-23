import { describe, expect, it } from "vitest";
import { FORM_FACTORS, formFactor, parseFormFactors } from "../src/form-factors.ts";

describe("form factors", () => {
  it("covers every shortty breakpoint band", () => {
    const widths = FORM_FACTORS.map((f) => f.width);
    expect(widths.some((w) => w > 1024)).toBe(true);
    expect(widths.some((w) => w > 640 && w < 1024)).toBe(true);
    expect(widths.some((w) => w < 640)).toBe(true);
  });

  it("captures physical 4K at the 4k-desktop profile", () => {
    const ff = formFactor("4k-desktop");
    expect(ff.width * ff.deviceScaleFactor).toBe(3840);
    expect(ff.height * ff.deviceScaleFactor).toBe(2160);
  });

  it("pairs a mobile user agent with the mobile viewport", () => {
    const mobile = formFactor("mobile");
    expect(mobile.userAgent).toContain("iPhone");
    expect(mobile.isMobile).toBe(true);
  });

  it("throws on unknown names, listing what exists", () => {
    expect(() => formFactor("desktop")).toThrow(/available: 4k-desktop/);
  });

  it("parses 'all', empty, and comma lists", () => {
    expect(parseFormFactors(undefined)).toEqual(FORM_FACTORS);
    expect(parseFormFactors("all")).toEqual(FORM_FACTORS);
    expect(parseFormFactors("laptop,mobile").map((f) => f.name)).toEqual(["laptop", "mobile"]);
  });
});

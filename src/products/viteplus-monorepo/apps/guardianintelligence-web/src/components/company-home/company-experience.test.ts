import { describe, expect, it } from "vite-plus/test";
import {
  COMPANY_EXPERIENCE_BOOTSTRAP,
  COMPANY_EXPERIENCE_CRITICAL_CSS,
  selectCompanyExperience,
} from "./company-experience";

describe("company experience policy", () => {
  it("animates without using startup latency as a device capability signal", () => {
    expect(
      selectCompanyExperience({ effectiveType: "4g", reducedMotion: false, saveData: false }),
    ).toEqual({ mode: "pending", reason: "eligible" });
    expect(COMPANY_EXPERIENCE_BOOTSTRAP).not.toContain("setTimeout");
    expect(COMPANY_EXPERIENCE_BOOTSTRAP).not.toContain("init-timeout");
    expect(COMPANY_EXPERIENCE_CRITICAL_CSS).toContain(
      'html[data-company-experience="pending"] .company-home-hero{visibility:hidden}',
    );
  });

  it("renders the complete static scene for reduced motion", () => {
    expect(
      selectCompanyExperience({ effectiveType: "4g", reducedMotion: true, saveData: false }),
    ).toEqual({ mode: "static", reason: "reduced-motion" });
  });

  it.each(["2g", "slow-2g"])("honors Save-Data-class connections (%s)", (effectiveType) => {
    expect(
      selectCompanyExperience({ effectiveType, reducedMotion: false, saveData: false }),
    ).toEqual({ mode: "static", reason: "save-data" });
  });

  it("honors an explicit Save-Data preference", () => {
    expect(
      selectCompanyExperience({ effectiveType: "4g", reducedMotion: false, saveData: true }),
    ).toEqual({ mode: "static", reason: "save-data" });
  });
});

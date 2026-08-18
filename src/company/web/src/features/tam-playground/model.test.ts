import { describe, expect, it } from "vitest";
import {
  DEFAULT_CURRENT_ENTHUSIAST_DEMAND_BILLION,
  DEFAULT_SEGMENT_CAGR_PCT,
  TAM_LEVERS,
  axisYears,
  clampLever,
  endpointOf,
  formatLeverValue,
  formatMonthYear,
  formatTamBillion,
  leverByParam,
  project,
  resolveInput,
  toCSV,
  validateTamSearch,
  type TamProjectionDefaults,
} from "./model";

// Mirrors the article's interactive defaults.
const DEFAULTS: TamProjectionDefaults = {
  currentCloudTamBillion: 723,
  cloudTam2030Billion: 2000,
  currentEnthusiastDemandBillion: DEFAULT_CURRENT_ENTHUSIAST_DEMAND_BILLION,
  segmentCagrPct: DEFAULT_SEGMENT_CAGR_PCT,
};

const defaultInput = resolveInput(DEFAULTS, {});

describe("weekly window", () => {
  it("samples weekly across 2026 -> 2030 and ends exactly on 2030", () => {
    const points = project(defaultInput);
    expect(points.length).toBeGreaterThan(200);
    expect(points.length).toBeLessThan(220);
    expect(points.at(0)?.t).toBeCloseTo(2026.0, 6);
    expect(points.at(-1)?.t).toBeCloseTo(2030.0, 6);
  });
});

describe("default scenario endpoint", () => {
  const endpoint = endpointOf(project(defaultInput));

  it("puts the standard projection at $2T", () => {
    expect(endpoint.standardProjectionTamBillion).toBeCloseTo(2000, 6);
    expect(formatTamBillion(endpoint.standardProjectionTamBillion)).toBe("$2T");
  });

  it("derives Segment CAGR from the $360B enthusiast opportunity", () => {
    expect(DEFAULT_SEGMENT_CAGR_PCT).toBeCloseTo(37.7, 1);
    expect(endpoint.enthusiastDemandBillion).toBeGreaterThan(359);
    expect(endpoint.enthusiastDemandBillion).toBeLessThan(361);
  });

  it("lands the Guardian projection at ~$2.4T", () => {
    expect(endpoint.guardianProjectionTamBillion).toBeGreaterThan(2359);
    expect(endpoint.guardianProjectionTamBillion).toBeLessThan(2361);
    expect(formatTamBillion(endpoint.guardianProjectionTamBillion)).toBe("$2.4T");
  });
});

describe("enthusiast demand is latent until 2027", () => {
  it("the demand band is closed before 2027, open after", () => {
    const points = project(defaultInput);
    for (const p of points) {
      if (p.t <= 2027) {
        expect(p.guardianProjectionTamBillion).toBeCloseTo(p.standardProjectionTamBillion, 6);
        expect(p.enthusiastDemandBillion).toBeCloseTo(0, 6);
      }
    }
    const endpoint = endpointOf(points);
    expect(endpoint.guardianProjectionTamBillion).toBeGreaterThan(
      endpoint.standardProjectionTamBillion + 300,
    );
  });
});

describe("the single lever", () => {
  it("is Segment CAGR, keyed to the existing enthusiast param", () => {
    expect(TAM_LEVERS).toHaveLength(1);
    expect(TAM_LEVERS[0]?.param).toBe("enthusiast");
    expect(TAM_LEVERS[0]?.label).toBe("Segment CAGR");
  });

  it("computes the range from the default", () => {
    const enthusiast = leverByParam("enthusiast");
    expect(enthusiast.min).toBeCloseTo(DEFAULT_SEGMENT_CAGR_PCT / 2, 1);
    expect(enthusiast.max).toBeCloseTo(DEFAULT_SEGMENT_CAGR_PCT * 5, 1);
    expect(enthusiast.step).toBe(0.1);
  });
});

describe("monotonic growth + ordering", () => {
  it("every series is non-decreasing, with Guardian >= standard", () => {
    const points = project(defaultInput);
    for (let i = 1; i < points.length; i += 1) {
      const prev = points[i - 1]!;
      const cur = points[i]!;
      expect(cur.standardProjectionTamBillion).toBeGreaterThanOrEqual(
        prev.standardProjectionTamBillion,
      );
      expect(cur.guardianProjectionTamBillion).toBeGreaterThanOrEqual(
        prev.guardianProjectionTamBillion,
      );
      expect(cur.guardianProjectionTamBillion).toBeGreaterThanOrEqual(
        cur.standardProjectionTamBillion,
      );
    }
  });
});

describe("clampLever", () => {
  const enthusiast = leverByParam("enthusiast");

  it("clamps and snaps to the decimal step", () => {
    expect(clampLever(enthusiast, -5)).toBe(enthusiast.min);
    expect(clampLever(enthusiast, 99999)).toBe(enthusiast.max);
    expect(clampLever(enthusiast, 37.84)).toBe(37.8);
    expect(clampLever(enthusiast, 37.85)).toBe(37.9);
    expect(clampLever(enthusiast, Number.NaN)).toBeUndefined();
  });
});

describe("validateTamSearch", () => {
  it("keeps the snapped enthusiast param and drops unknowns", () => {
    expect(validateTamSearch({ enthusiast: 38.04, junk: "x" })).toEqual({ enthusiast: 38 });
    expect(validateTamSearch({ enthusiast: "abc" })).toEqual({});
    expect(validateTamSearch({})).toEqual({});
    expect(resolveInput(DEFAULTS, validateTamSearch({}))).toEqual(DEFAULTS);
  });
});

describe("axis + formatting", () => {
  it("ticks every year from 2026 through 2030", () => {
    expect(axisYears()).toEqual([2026, 2027, 2028, 2029, 2030]);
  });

  it("formats month-year, dollars, and lever values", () => {
    const enthusiast = leverByParam("enthusiast");
    expect(formatMonthYear(2026.0)).toBe("Jan '26");
    expect(formatMonthYear(2027.0)).toBe("Jan '27");
    expect(formatTamBillion(360)).toBe("$360B");
    expect(formatTamBillion(2000)).toBe("$2T");
    expect(formatTamBillion(2360)).toBe("$2.4T");
    expect(formatLeverValue(enthusiast, DEFAULT_SEGMENT_CAGR_PCT)).toBe("37.7%");
  });
});

describe("toCSV", () => {
  it("emits a header (standard, Guardian, demand) and one row per weekly point", () => {
    const points = project(defaultInput);
    const csv = toCSV(points);
    const lines = csv.trimEnd().split("\n");
    expect(lines[0]).toBe(
      "year_fraction,standard_projection_billion,guardian_projection_billion,enthusiast_demand_billion",
    );
    expect(lines).toHaveLength(points.length + 1);
  });
});

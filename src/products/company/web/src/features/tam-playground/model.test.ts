import { describe, expect, it } from "vitest";
import {
  TAM_LEVERS,
  clampLever,
  endpointOf,
  formatQuarterShort,
  formatTamBillion,
  leverByParam,
  project,
  resolveInput,
  toCSV,
  validateTamSearch,
  type TamProjectionDefaults,
} from "./model";

// Mirrors the article's interactive defaults. The headline claim is a property
// of these numbers, so the suite asserts against them directly.
const DEFAULTS: TamProjectionDefaults = {
  currentCloudTamBillion: 723,
  cloudTam2030Billion: 2500,
  hobbyistGrowthPct: 7,
  softwareFactoryGrowthPct: 200,
  currentBareMetalTamBillion: 100,
  startQuarter: "2026Q3",
  endQuarter: "2030Q4",
};

const defaultInput = resolveInput(DEFAULTS, {});

describe("quarter window", () => {
  it("spans 2026Q3 through 2030Q4 inclusive", () => {
    const points = project(defaultInput);
    expect(points).toHaveLength(18);
    expect(points.at(0)?.quarter).toBe("2026Q3");
    expect(points.at(-1)?.quarter).toBe("2030Q4");
  });
});

describe("default scenario endpoint", () => {
  const endpoint = endpointOf(project(defaultInput));

  it("rounds the software-factory line to $1T", () => {
    // ~$1.06T under defaults; the copy rounds to $1T, the formatter shows $1.06T.
    expect(endpoint.defaultSoftwareCompanyTamBillion).toBeGreaterThan(1000);
    expect(endpoint.defaultSoftwareCompanyTamBillion).toBeLessThan(1100);
    expect(formatTamBillion(endpoint.defaultSoftwareCompanyTamBillion)).toBe("$1.06T");
  });

  it("indexes bare metal to cloud TAM at 2030", () => {
    // 100 * 2500 / 723
    expect(endpoint.cloudIndexedBareMetalTamBillion).toBeCloseTo(345.78, 1);
    expect(endpoint.cloudTamBillion).toBeCloseTo(2500, 6);
  });

  it("stacks hobbyist uplift over the cloud-indexed line", () => {
    const upliftAtEnd =
      endpoint.pcBuilderTamBillion - endpoint.cloudIndexedBareMetalTamBillion;
    // 7% of the 2030 cloud-indexed line.
    expect(upliftAtEnd).toBeCloseTo(345.78 * 0.07, 1);
  });
});

describe("lever bounds", () => {
  it("drives the endpoint up at the upper bound and down at the lower bound", () => {
    const low = endpointOf(
      project(
        resolveInput(DEFAULTS, { cloudNow: 2000, cloud2030: 1000, hobby: 0, factory: 0 }),
      ),
    );
    const high = endpointOf(
      project(
        resolveInput(DEFAULTS, { cloudNow: 250, cloud2030: 6000, hobby: 50, factory: 500 }),
      ),
    );
    expect(high.defaultSoftwareCompanyTamBillion).toBeGreaterThan(
      low.defaultSoftwareCompanyTamBillion,
    );
    // Lower bound: cloud shrinks, no displacement, no factory step -> the three
    // lines collapse onto the cloud-indexed baseline.
    expect(low.pcBuilderTamBillion).toBeCloseTo(low.cloudIndexedBareMetalTamBillion, 6);
    expect(low.defaultSoftwareCompanyTamBillion).toBeCloseTo(
      low.cloudIndexedBareMetalTamBillion,
      6,
    );
  });

  it("zero software-factory growth keeps the thesis line on the hobbyist line", () => {
    const endpoint = endpointOf(project(resolveInput(DEFAULTS, { factory: 0 })));
    expect(endpoint.defaultSoftwareCompanyTamBillion).toBeCloseTo(
      endpoint.pcBuilderTamBillion,
      6,
    );
  });
});

describe("monotonic growth", () => {
  it("every series is non-decreasing across the window under defaults", () => {
    const points = project(defaultInput);
    for (let i = 1; i < points.length; i += 1) {
      const prev = points[i - 1]!;
      const cur = points[i]!;
      expect(cur.cloudTamBillion).toBeGreaterThanOrEqual(prev.cloudTamBillion);
      expect(cur.cloudIndexedBareMetalTamBillion).toBeGreaterThanOrEqual(
        prev.cloudIndexedBareMetalTamBillion,
      );
      expect(cur.pcBuilderTamBillion).toBeGreaterThanOrEqual(prev.pcBuilderTamBillion);
      expect(cur.defaultSoftwareCompanyTamBillion).toBeGreaterThanOrEqual(
        prev.defaultSoftwareCompanyTamBillion,
      );
    }
  });

  it("orders the three lines: cloud-indexed <= hobbyist <= software factory", () => {
    for (const point of project(defaultInput)) {
      expect(point.pcBuilderTamBillion).toBeGreaterThanOrEqual(
        point.cloudIndexedBareMetalTamBillion,
      );
      expect(point.defaultSoftwareCompanyTamBillion).toBeGreaterThanOrEqual(
        point.pcBuilderTamBillion,
      );
    }
  });
});

describe("clampLever", () => {
  it("clamps below min and above max", () => {
    const cloudNow = leverByParam("cloudNow");
    expect(clampLever(cloudNow, -1)).toBe(cloudNow.min);
    expect(clampLever(cloudNow, 99999)).toBe(cloudNow.max);
  });

  it("snaps to the lever step", () => {
    const cloud2030 = leverByParam("cloud2030"); // step 50
    expect(clampLever(cloud2030, 2511)).toBe(2500);
    expect(clampLever(cloud2030, 2530)).toBe(2550);
  });

  it("rejects non-finite values", () => {
    expect(clampLever(leverByParam("hobby"), Number.NaN)).toBeUndefined();
  });
});

describe("validateTamSearch", () => {
  it("keeps valid params, snapped, and drops unknowns", () => {
    const out = validateTamSearch({
      cloudNow: 731,
      cloud2030: "2500",
      hobby: 7,
      factory: 200,
      junk: "drop me",
    });
    expect(out).toEqual({ cloudNow: 725, cloud2030: 2500, hobby: 7, factory: 200 });
  });

  it("returns an empty object for absent/garbage input", () => {
    expect(validateTamSearch({})).toEqual({});
    expect(validateTamSearch({ cloudNow: "abc" })).toEqual({});
  });

  it("round-trips every lever param", () => {
    for (const spec of TAM_LEVERS) {
      const out = validateTamSearch({ [spec.param]: spec.min });
      expect(out[spec.param]).toBe(spec.min);
    }
  });
});

describe("formatting", () => {
  it("formats billions and trillions", () => {
    expect(formatTamBillion(345.78)).toBe("$346B");
    expect(formatTamBillion(1061.5)).toBe("$1.06T");
  });

  it("compacts quarters", () => {
    expect(formatQuarterShort("2026Q3")).toBe("Q3 '26");
    expect(formatQuarterShort("2030Q4")).toBe("Q4 '30");
  });
});

describe("toCSV", () => {
  it("emits a header and one row per quarter", () => {
    const csv = toCSV(project(defaultInput));
    const lines = csv.trimEnd().split("\n");
    expect(lines[0]).toBe(
      "quarter,cloud_tam_billion,cloud_indexed_bare_metal_billion,with_hobbyist_billion,software_factory_billion",
    );
    expect(lines).toHaveLength(19); // header + 18 quarters
    expect(lines[1]?.startsWith("2026Q3,")).toBe(true);
  });
});

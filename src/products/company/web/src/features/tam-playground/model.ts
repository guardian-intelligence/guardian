// Direct-to-consumer bare metal TAM — pure projection model.
//
// This module owns ALL of the math and none of the rendering. It is imported
// by the SSR summary table, the client canvas chart, the URL-state controls,
// and the unit tests. Keeping it pure (no React, no DOM, no time source) is
// what lets the same numbers render identically on the server and the client
// and lets the test suite pin the headline claim.
//
// Scenario, not forecast. The article frames these as explicit, reader-editable
// assumptions. Four levers are exposed:
//
//   1. currentCloudTamBillion  — today's cloud TAM            (cloudNow)
//   2. cloudTam2030Billion     — projected 2030 cloud TAM     (cloud2030)
//   3. hobbyistGrowthPct       — hobbyist/PC-builder market   (hobby)
//   4. softwareFactoryGrowthPct— new-software-company market  (factory)
//
// Levers 3 and 4 replace the two fixed secondary assumptions from the original
// note: the "+$25B PC-builder uplift" becomes a percentage of the 2030
// cloud-indexed line, and the "3x default-company multiplier" becomes a
// percentage growth (200% == the old 3x). The current bare-metal baseline
// ($100B) stays a fixed assumption.
//
// Under the defaults the software-factory endpoint lands at ~$1.06T, which the
// article copy rounds to $1T:
//
//   cloudIndexed2030 = 100 * 2500 / 723          = ~$345.8B
//   hobbyist uplift  = 345.8 * 7%                 = ~$24.2B
//   factory step     = 345.8 * 200%               = ~$691.6B
//   endpoint         = 345.8 + 24.2 + 691.6       = ~$1.06T  ✓

export interface TamProjectionDefaults {
  readonly currentCloudTamBillion: number;
  readonly cloudTam2030Billion: number;
  readonly hobbyistGrowthPct: number;
  readonly softwareFactoryGrowthPct: number;
  readonly currentBareMetalTamBillion: number;
  readonly startQuarter: "2026Q3";
  readonly endQuarter: "2030Q4";
}

// The fully-resolved set of numbers the projection consumes. Lever values come
// from the URL (or the article defaults); the bare-metal baseline is fixed.
export interface TamProjectionInput {
  readonly currentCloudTamBillion: number;
  readonly cloudTam2030Billion: number;
  readonly hobbyistGrowthPct: number;
  readonly softwareFactoryGrowthPct: number;
  readonly currentBareMetalTamBillion: number;
}

export interface TamProjectionPoint {
  readonly quarter: string;
  readonly cloudTamBillion: number;
  readonly cloudIndexedBareMetalTamBillion: number;
  readonly pcBuilderTamBillion: number;
  readonly defaultSoftwareCompanyTamBillion: number;
}

// --- Quarter arithmetic -----------------------------------------------------
// Quarters are encoded as a single integer (year*4 + quarterOrdinal) so ramps
// and interpolation are plain arithmetic. 2026Q3 is the start of the window.

const START_YEAR = 2026;
const START_Q = 3;
const END_YEAR = 2030;
const END_Q = 4;

function quarterIndex(year: number, quarter: number): number {
  return year * 4 + (quarter - 1);
}

function indexToQuarter(index: number): string {
  const year = Math.floor(index / 4);
  const quarter = (index % 4) + 1;
  return `${year}Q${quarter}`;
}

const START_INDEX = quarterIndex(START_YEAR, START_Q);
const END_INDEX = quarterIndex(END_YEAR, END_Q);
const POINT_COUNT = END_INDEX - START_INDEX + 1; // 18 quarters, inclusive.

// PC-builder/hobbyist displacement ramps in across the whole window; the
// software-factory step function only begins to bite once new companies are
// defaulting to hosted bare metal in 2028+.
const HOBBYIST_RAMP_START = quarterIndex(2026, 4);
const FACTORY_RAMP_START = quarterIndex(2028, 4);

// Smoothstep — a C1-continuous 0→1 ramp. Used so the displacement and
// step-function lines ease in rather than turning on with a kink.
function smoothstep(edge0: number, edge1: number, x: number): number {
  if (edge1 <= edge0) return x >= edge1 ? 1 : 0;
  const t = Math.min(1, Math.max(0, (x - edge0) / (edge1 - edge0)));
  return t * t * (3 - 2 * t);
}

// --- Projection -------------------------------------------------------------

export function project(input: TamProjectionInput): readonly TamProjectionPoint[] {
  const {
    currentCloudTamBillion,
    cloudTam2030Billion,
    hobbyistGrowthPct,
    softwareFactoryGrowthPct,
    currentBareMetalTamBillion,
  } = input;

  // Cloud TAM grows exponentially (constant CAGR) from now to 2030. The ratio
  // also indexes the bare-metal line, so guard the degenerate zero case.
  const ratio =
    currentCloudTamBillion > 0 ? cloudTam2030Billion / currentCloudTamBillion : 1;

  // Endpoints computed once at 2030Q4 and ramped back in, so the headline
  // numbers are exact regardless of the smoothstep shape.
  const cloudIndexed2030 = currentBareMetalTamBillion * ratio;
  const hobbyistUplift2030 = cloudIndexed2030 * (hobbyistGrowthPct / 100);
  const factoryStep2030 = cloudIndexed2030 * (softwareFactoryGrowthPct / 100);

  const points: TamProjectionPoint[] = [];
  for (let i = 0; i < POINT_COUNT; i += 1) {
    const index = START_INDEX + i;
    const fraction = POINT_COUNT === 1 ? 1 : i / (POINT_COUNT - 1);

    const cloudTamBillion = currentCloudTamBillion * Math.pow(ratio, fraction);
    const cloudIndexedBareMetalTamBillion =
      currentCloudTamBillion > 0
        ? (currentBareMetalTamBillion * cloudTamBillion) / currentCloudTamBillion
        : currentBareMetalTamBillion;

    const hobbyistUplift = hobbyistUplift2030 * smoothstep(HOBBYIST_RAMP_START, END_INDEX, index);
    const pcBuilderTamBillion = cloudIndexedBareMetalTamBillion + hobbyistUplift;

    const factoryStep = factoryStep2030 * smoothstep(FACTORY_RAMP_START, END_INDEX, index);
    const defaultSoftwareCompanyTamBillion = pcBuilderTamBillion + factoryStep;

    points.push({
      quarter: indexToQuarter(index),
      cloudTamBillion,
      cloudIndexedBareMetalTamBillion,
      pcBuilderTamBillion,
      defaultSoftwareCompanyTamBillion,
    });
  }
  return points;
}

export function endpointOf(points: readonly TamProjectionPoint[]): TamProjectionPoint {
  const last = points[points.length - 1];
  if (!last) throw new Error("projection produced no points");
  return last;
}

// --- Series metadata --------------------------------------------------------
// Single source of truth for the three plotted lines. The chart, the legend,
// the table headers, and the CSV columns all derive from this so they cannot
// drift apart.

export type TamSeriesKey =
  | "cloudIndexedBareMetalTamBillion"
  | "pcBuilderTamBillion"
  | "defaultSoftwareCompanyTamBillion";

export interface TamSeriesSpec {
  readonly key: TamSeriesKey;
  readonly label: string;
  readonly short: string;
  readonly csvColumn: string;
  // Emphasis tier drives line weight and whether the Flare accent is allowed.
  // Only the thesis line is emphasized; Flare stays a bounded event.
  readonly emphasis: "base" | "mid" | "thesis";
}

export const TAM_SERIES: readonly TamSeriesSpec[] = [
  {
    key: "cloudIndexedBareMetalTamBillion",
    label: "Cloud-indexed bare metal TAM",
    short: "Cloud-indexed",
    csvColumn: "cloud_indexed_bare_metal_billion",
    emphasis: "base",
  },
  {
    key: "pcBuilderTamBillion",
    label: "With hobbyist displacement",
    short: "+ Hobbyist",
    csvColumn: "with_hobbyist_billion",
    emphasis: "mid",
  },
  {
    key: "defaultSoftwareCompanyTamBillion",
    label: "Default for new software companies",
    short: "Software factory",
    csvColumn: "software_factory_billion",
    emphasis: "thesis",
  },
];

// --- Levers -----------------------------------------------------------------

export type TamLeverParam = "cloudNow" | "cloud2030" | "hobby" | "factory";

export type TamLeverKey =
  | "currentCloudTamBillion"
  | "cloudTam2030Billion"
  | "hobbyistGrowthPct"
  | "softwareFactoryGrowthPct";

export interface TamLeverSpec {
  readonly key: TamLeverKey;
  readonly param: TamLeverParam;
  readonly label: string;
  readonly help: string;
  readonly unit: "$B" | "%";
  readonly min: number;
  readonly max: number;
  readonly step: number;
}

export const TAM_LEVERS: readonly TamLeverSpec[] = [
  {
    key: "currentCloudTamBillion",
    param: "cloudNow",
    label: "Current cloud TAM",
    help: "Today's total cloud market.",
    unit: "$B",
    min: 250,
    max: 2000,
    step: 25,
  },
  {
    key: "cloudTam2030Billion",
    param: "cloud2030",
    label: "2030 cloud TAM",
    help: "Projected cloud market by 2030Q4.",
    unit: "$B",
    min: 1000,
    max: 6000,
    step: 50,
  },
  {
    key: "hobbyistGrowthPct",
    param: "hobby",
    label: "Hobbyist market growth",
    help: "Consumer/PC-builder demand displaced into hosted bare metal, as a share of the 2030 cloud-indexed line.",
    unit: "%",
    min: 0,
    max: 50,
    step: 1,
  },
  {
    key: "softwareFactoryGrowthPct",
    param: "factory",
    label: "Software factory growth",
    help: "New software companies defaulting to hosted bare metal, as growth over the 2030 cloud-indexed line. 200% == 3x.",
    unit: "%",
    min: 0,
    max: 500,
    step: 10,
  },
];

export function leverByParam(param: TamLeverParam): TamLeverSpec {
  const spec = TAM_LEVERS.find((lever) => lever.param === param);
  if (!spec) throw new Error(`unknown lever param: ${param}`);
  return spec;
}

// Clamp a candidate value into the lever's valid range and snap it to the
// lever's step. Returns undefined for non-finite input so the URL never picks
// up NaN. Snapping is anchored at min so steps line up with the slider stops.
export function clampLever(spec: TamLeverSpec, value: number): number | undefined {
  if (!Number.isFinite(value)) return undefined;
  const bounded = Math.min(spec.max, Math.max(spec.min, value));
  const snapped = Math.round((bounded - spec.min) / spec.step) * spec.step + spec.min;
  return Math.min(spec.max, Math.max(spec.min, snapped));
}

export function defaultLeverValue(defaults: TamProjectionDefaults, spec: TamLeverSpec): number {
  return defaults[spec.key];
}

// --- URL search state -------------------------------------------------------

export interface TamSearch {
  readonly cloudNow?: number;
  readonly cloud2030?: number;
  readonly hobby?: number;
  readonly factory?: number;
}

// validateSearch authority for the route. Accepts the raw search record,
// coerces each known param to a clamped/snapped number, and drops everything
// else. Unknown or invalid values vanish rather than throwing so a hand-edited
// share link degrades to the defaults instead of 404-ing.
export function validateTamSearch(raw: Record<string, unknown>): TamSearch {
  const out: { -readonly [K in keyof TamSearch]: TamSearch[K] } = {};
  for (const spec of TAM_LEVERS) {
    const value = raw[spec.param];
    if (value === undefined || value === null || value === "") continue;
    const numeric = typeof value === "number" ? value : Number(value);
    const clamped = clampLever(spec, numeric);
    if (clamped !== undefined) out[spec.param] = clamped;
  }
  return out;
}

export function resolveInput(
  defaults: TamProjectionDefaults,
  search: TamSearch,
): TamProjectionInput {
  return {
    currentCloudTamBillion: search.cloudNow ?? defaults.currentCloudTamBillion,
    cloudTam2030Billion: search.cloud2030 ?? defaults.cloudTam2030Billion,
    hobbyistGrowthPct: search.hobby ?? defaults.hobbyistGrowthPct,
    softwareFactoryGrowthPct: search.factory ?? defaults.softwareFactoryGrowthPct,
    currentBareMetalTamBillion: defaults.currentBareMetalTamBillion,
  };
}

export function leverValue(input: TamProjectionInput, spec: TamLeverSpec): number {
  return input[spec.key];
}

// --- Formatting -------------------------------------------------------------

// Dollars rendered the way the article talks: billions until the number
// crosses a trillion, then two-decimal trillions ("$1.06T").
export function formatTamBillion(value: number): string {
  if (value >= 1000) return `$${(value / 1000).toFixed(2)}T`;
  return `$${Math.round(value)}B`;
}

// "2026Q3" -> "Q3 '26" for compact axis labels.
export function formatQuarterShort(quarter: string): string {
  const match = /^(\d{4})Q([1-4])$/.exec(quarter);
  const year = match?.[1];
  const q = match?.[2];
  if (!year || !q) return quarter;
  return `Q${q} '${year.slice(2)}`;
}

export function formatLeverValue(spec: TamLeverSpec, value: number): string {
  return spec.unit === "%" ? `${value}%` : formatTamBillion(value);
}

// --- CSV --------------------------------------------------------------------

function round2(value: number): number {
  return Math.round(value * 100) / 100;
}

export function toCSV(points: readonly TamProjectionPoint[]): string {
  const header = ["quarter", "cloud_tam_billion", ...TAM_SERIES.map((s) => s.csvColumn)];
  const rows = points.map((point) =>
    [
      point.quarter,
      round2(point.cloudTamBillion),
      ...TAM_SERIES.map((s) => round2(point[s.key])),
    ].join(","),
  );
  return [header.join(","), ...rows].join("\n") + "\n";
}

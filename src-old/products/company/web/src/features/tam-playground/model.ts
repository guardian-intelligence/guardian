// Cloud / enthusiast TAM — pure projection model.
//
// This module owns ALL of the math and none of the rendering. It is imported by
// the client canvas chart, the URL-state controls, the playground composition,
// and the unit tests. Keeping it pure (no React, no DOM, no time source) is what
// lets the same numbers render identically on the server and client.
//
// Scenario, not forecast. ONE reader-editable lever:
//
//   segmentCagrPct — "Segment CAGR"  (URL param: enthusiast)
//
// Everything else is a fixed assumption. The standard cloud projection grows
// from Gartner's $723B 2025 estimate to Goldman Sachs' $2T 2030 projection. The
// Guardian projection adds a latent enthusiast segment that begins landing in
// 2027. Under the default:
//
//   standard projection = $723B (2026) -> $2.0T (2030)
//   segment baseline    = $100B (2026)
//   segment CAGR        = (($360B / $100B) ^ (1 / 4 years)) - 1 = ~37.7%
//   Guardian projection = $2.0T + $360B = ~$2.4T (2030)
//
// The series are sampled WEEKLY across the window (2025 → 2030) so the lines
// read as smooth curves rather than chunky steps.

const START_T = 2026.0;
const END_T = 2030.0;
const WEEKS_PER_YEAR = 52;
const POINT_COUNT = Math.round((END_T - START_T) * WEEKS_PER_YEAR) + 1;

const RAMP_START = 2027.0;
const RAMP_END = END_T;
const SEGMENT_CAGR_YEARS = END_T - START_T;
const SEGMENT_CAGR_STEP = 0.1;

export const DEFAULT_CURRENT_ENTHUSIAST_DEMAND_BILLION = 100;
export const DEFAULT_2030_ENTHUSIAST_OPPORTUNITY_BILLION = 360;

export interface TamProjectionDefaults {
  readonly currentCloudTamBillion: number;
  readonly cloudTam2030Billion: number;
  readonly currentEnthusiastDemandBillion: number;
  readonly segmentCagrPct: number;
}

export interface TamProjectionInput {
  readonly currentCloudTamBillion: number;
  readonly cloudTam2030Billion: number;
  readonly currentEnthusiastDemandBillion: number;
  readonly segmentCagrPct: number;
}

export interface TamProjectionPoint {
  // Continuous year fraction, e.g. 2027.25 ≈ early April 2027.
  readonly t: number;
  readonly standardProjectionTamBillion: number;
  readonly guardianProjectionTamBillion: number;
  readonly enthusiastDemandBillion: number;
}

// Smoothstep — a C1-continuous 0→1 ramp, the S-curve the enthusiast line eases
// along instead of turning on with a kink.
function smoothstep(x: number): number {
  const t = Math.min(1, Math.max(0, x));
  return t * t * (3 - 2 * t);
}

function roundToStep(value: number, step: number): number {
  const decimals = Math.max(0, (String(step).split(".")[1] ?? "").length);
  return Number((Math.round(value / step) * step).toFixed(decimals));
}

export function segmentCagrPctForOpportunity(
  currentSegmentBillion: number,
  opportunity2030Billion: number,
): number {
  if (currentSegmentBillion <= 0 || opportunity2030Billion <= 0) return 0;
  const rate =
    (Math.pow(opportunity2030Billion / currentSegmentBillion, 1 / SEGMENT_CAGR_YEARS) - 1) *
    100;
  return roundToStep(rate, SEGMENT_CAGR_STEP);
}

export const DEFAULT_SEGMENT_CAGR_PCT = segmentCagrPctForOpportunity(
  DEFAULT_CURRENT_ENTHUSIAST_DEMAND_BILLION,
  DEFAULT_2030_ENTHUSIAST_OPPORTUNITY_BILLION,
);

// --- Projection -------------------------------------------------------------

export function project(input: TamProjectionInput): readonly TamProjectionPoint[] {
  const {
    currentCloudTamBillion,
    cloudTam2030Billion,
    currentEnthusiastDemandBillion,
    segmentCagrPct,
  } = input;

  // The standard projection grows exponentially across the window. The
  // enthusiast segment also compounds, but only shows up in the visible market
  // once the 2027 adoption ramp begins.
  const ratio =
    currentCloudTamBillion > 0 ? cloudTam2030Billion / currentCloudTamBillion : 1;
  const segmentRate = 1 + segmentCagrPct / 100;

  const points: TamProjectionPoint[] = [];
  for (let i = 0; i < POINT_COUNT; i += 1) {
    const f = POINT_COUNT === 1 ? 1 : i / (POINT_COUNT - 1);
    const t = START_T + (END_T - START_T) * f;
    const yearsFromStart = t - START_T;

    const standardProjectionTamBillion = currentCloudTamBillion * Math.pow(ratio, f);
    const segmentPotentialBillion =
      currentEnthusiastDemandBillion * Math.pow(segmentRate, yearsFromStart);
    const ramp = smoothstep((t - RAMP_START) / (RAMP_END - RAMP_START));
    const enthusiastDemandBillion = segmentPotentialBillion * ramp;
    const guardianProjectionTamBillion = standardProjectionTamBillion + enthusiastDemandBillion;

    points.push({
      t,
      standardProjectionTamBillion,
      guardianProjectionTamBillion,
      enthusiastDemandBillion,
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
// Single source of truth for the two plotted lines. The chart, legend, KPIs,
// and CSV columns all derive from this so they cannot drift apart.

export type TamSeriesKey =
  | "standardProjectionTamBillion"
  | "guardianProjectionTamBillion";

export interface TamSeriesSpec {
  readonly key: TamSeriesKey;
  readonly label: string;
  readonly short: string;
  readonly csvColumn: string;
  // reference = the cloud projection (lighter dashed context line);
  // thesis = the Guardian projection (heavy, with the bounded Flare band).
  readonly emphasis: "reference" | "thesis";
}

export const TAM_SERIES: readonly TamSeriesSpec[] = [
  {
    key: "standardProjectionTamBillion",
    label: "Standard Projection",
    short: "Standard",
    csvColumn: "standard_projection_billion",
    emphasis: "reference",
  },
  {
    key: "guardianProjectionTamBillion",
    label: "Guardian Projection",
    short: "Guardian",
    csvColumn: "guardian_projection_billion",
    emphasis: "thesis",
  },
];

// --- Lever ------------------------------------------------------------------

export type TamLeverParam = "enthusiast";
export type TamLeverKey = "segmentCagrPct";

export interface TamLeverSpec {
  readonly key: TamLeverKey;
  readonly param: TamLeverParam;
  readonly label: string;
  readonly help: string;
  readonly unit: "%";
  readonly min: number;
  readonly max: number;
  readonly step: number;
}

export const TAM_LEVERS: readonly TamLeverSpec[] = [
  {
    key: "segmentCagrPct",
    param: "enthusiast",
    label: "Segment CAGR",
    help: "",
    unit: "%",
    min: roundToStep(DEFAULT_SEGMENT_CAGR_PCT * 0.5, SEGMENT_CAGR_STEP),
    max: roundToStep(DEFAULT_SEGMENT_CAGR_PCT * 5, SEGMENT_CAGR_STEP),
    step: SEGMENT_CAGR_STEP,
  },
];

export function leverByParam(param: TamLeverParam): TamLeverSpec {
  const spec = TAM_LEVERS.find((lever) => lever.param === param);
  if (!spec) throw new Error(`unknown lever param: ${param}`);
  return spec;
}

// Clamp a candidate into the lever's range and snap to its step. Returns
// undefined for non-finite input so the URL never picks up NaN.
export function clampLever(spec: TamLeverSpec, value: number): number | undefined {
  if (!Number.isFinite(value)) return undefined;
  const bounded = Math.min(spec.max, Math.max(spec.min, value));
  const snapped = roundToStep(
    Math.round((bounded - spec.min) / spec.step) * spec.step + spec.min,
    spec.step,
  );
  return Math.min(spec.max, Math.max(spec.min, snapped));
}

export function defaultLeverValue(defaults: TamProjectionDefaults, spec: TamLeverSpec): number {
  return defaults[spec.key];
}

// --- URL search state -------------------------------------------------------

export interface TamSearch {
  readonly enthusiast?: number;
}

// validateSearch authority for the route. Coerces the known param to a
// clamped/snapped number and drops everything else; an out-of-range hand-edited
// link degrades to the default instead of 404-ing.
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
  const legacyDefaults = defaults as TamProjectionDefaults & {
    readonly currentBareMetalTamBillion?: number;
    readonly enthusiastGrowthPct?: number;
  };
  const currentEnthusiastDemandBillion =
    defaults.currentEnthusiastDemandBillion ??
    legacyDefaults.currentBareMetalTamBillion ??
    DEFAULT_CURRENT_ENTHUSIAST_DEMAND_BILLION;
  const segmentCagrPct =
    search.enthusiast ??
    defaults.segmentCagrPct ??
    legacyDefaults.enthusiastGrowthPct ??
    DEFAULT_SEGMENT_CAGR_PCT;
  return {
    currentCloudTamBillion: defaults.currentCloudTamBillion,
    cloudTam2030Billion: defaults.cloudTam2030Billion,
    currentEnthusiastDemandBillion,
    segmentCagrPct,
  };
}

export function leverValue(input: TamProjectionInput, spec: TamLeverSpec): number {
  return input[spec.key] ?? DEFAULT_SEGMENT_CAGR_PCT;
}

// --- Formatting -------------------------------------------------------------

// Dollars the way the article talks: billions until a trillion, then rounded
// trillions ("$2T", "$2.4T").
export function formatTamBillion(value: number): string {
  if (value >= 1000) {
    const rounded = Math.round((value / 1000) * 10) / 10;
    return `$${Number.isInteger(rounded) ? rounded.toFixed(0) : rounded.toFixed(1)}T`;
  }
  return `$${Math.round(value)}B`;
}

const MONTHS = [
  "Jan",
  "Feb",
  "Mar",
  "Apr",
  "May",
  "Jun",
  "Jul",
  "Aug",
  "Sep",
  "Oct",
  "Nov",
  "Dec",
] as const;

// Year fraction -> "Mon ''YY" (e.g. 2027.25 -> "Apr '27") for the hover readout.
export function formatMonthYear(t: number): string {
  const year = Math.floor(t);
  const monthIndex = Math.min(11, Math.max(0, Math.floor((t - year) * 12)));
  return `${MONTHS[monthIndex]} '${String(year).slice(2)}`;
}

// Integer years that fall inside the window, for sparse x-axis ticks.
export function axisYears(): readonly number[] {
  const years: number[] = [];
  for (let y = Math.ceil(START_T); y <= Math.floor(END_T); y += 1) years.push(y);
  return years;
}

export const WINDOW = { startT: START_T, endT: END_T } as const;

export function formatLeverValue(spec: TamLeverSpec, value: number): string {
  const safeValue = Number.isFinite(value) ? value : DEFAULT_SEGMENT_CAGR_PCT;
  const formatted = Number.isInteger(safeValue) ? safeValue.toFixed(0) : safeValue.toFixed(1);
  return `${formatted}${spec.unit}`;
}

// --- CSV --------------------------------------------------------------------

function round2(value: number): number {
  return Math.round(value * 100) / 100;
}

export function toCSV(points: readonly TamProjectionPoint[]): string {
  const header = [
    "year_fraction",
    ...TAM_SERIES.map((s) => s.csvColumn),
    "enthusiast_demand_billion",
  ];
  const rows = points.map((point) =>
    [
      round2(point.t),
      ...TAM_SERIES.map((s) => round2(point[s.key])),
      round2(point.enthusiastDemandBillion),
    ].join(","),
  );
  return [header.join(","), ...rows].join("\n") + "\n";
}

// Roadmap, Oct 2026 to Dec 2031: gross ARR by revenue stream, built from
// the driver math in the Phase 0-2 forecast. Phase 0-1 streams are bottom-up
// (collectors x accepted hours x price); Phase 2 is illustrative. Data is the
// only product: every stream is licensed data, labels or evals.
//
// Time is a fractional calendar year: 2027.0 is 1 Jan 2027, and a month's
// value is read at its end (Mar 2027 is 2027.25). Gross ARR is trailing-month
// recognised revenue x 12 from signed contracts (gross billings). Net revenue
// is gross minus direct payouts to agencies/collectors, farms and annotators.
//
// Each stream rises on a start-anchored logistic: zero on its start date,
// then cap * (L(t) - L(start)) / (1 - L(start)), so nothing steps in. Agency
// onboarding eases in over the stream's first year (smoothstep), which keeps
// the early months below Mercor's pace. The Phase 2 farm data network grows
// at a constant onboarding pace instead.

export type PhaseId = "p0" | "p1" | "p2";

export type Phase = {
  readonly id: PhaseId;
  readonly number: number;
  readonly name: string;
  // Opens at the start of this fractional year.
  readonly start: number;
  // Gate (exit) at the end of this fractional year.
  readonly end: number;
  // Window as the slides print it.
  readonly window: string;
};

export const PHASES = [
  {
    id: "p0",
    number: 0,
    name: "Prove it",
    start: 2026 + 10 / 12,
    end: 2027.25,
    window: "Nov 2026 – Mar 2027",
  },
  {
    id: "p1",
    number: 1,
    name: "Scale",
    start: 2027.25,
    end: 2029.5,
    window: "Apr 2027 – Jun 2029",
  },
  {
    id: "p2",
    number: 2,
    name: "Span the country",
    start: 2029.5,
    end: 2032,
    window: "Jul 2029 – Dec 2031",
  },
] as const satisfies readonly Phase[];

export type StreamId =
  | "agency"
  | "mobility"
  | "custom"
  | "annotation"
  | "catalogue"
  | "premium"
  | "bench"
  | "farmdata";

export type Stream = {
  readonly id: StreamId;
  readonly phase: PhaseId;
  // Short label for the chart legend.
  readonly name: string;
  readonly start: number;
  // Gross ARR at saturation.
  readonly cap: number;
  // Year the un-anchored logistic reaches half its cap.
  readonly mid: number;
  // Logistic steepness, per year.
  readonly rate: number;
  // Net revenue / gross.
  readonly netShare: number;
  // Years over which onboarding eases in (smoothstep), so a stream leaves
  // zero with zero slope instead of a kink; 0 for none.
  readonly ease: number;
  // Upside case: a ceiling `value` reached at `at` along the forecast's
  // stated upside path and held there (see streamUpside), or a plain
  // multiple of the plan when only the price differs.
  readonly upside: { readonly value: number; readonly at: number } | { readonly times: number };
  // Onboarding-paced streams grow linearly at `perYear` gross ARR a year
  // after easing in, instead of following the logistic.
  readonly perYear?: number;
};

// Upside drivers are stated at Mar 2030 (month 36 from the first $1M month).
const UPSIDE_AT = 2030.25;
// Phase 0 agency curve midpoint, tuned so the plan first reaches $1.0M in
// Mar 2027.
const AGENCY_MID = 2027.155;
// Phase 2 partner onboarding pace and gross ARR per partner holding
// (40 hrs x $30).
const HOLDINGS_PER_MONTH = 2200;
const HOLDING_ARR = 40 * 30;

// Bottom of the stack first.
export const STREAMS = [
  // Phase 0 gate at Mar 2027: (5,000 ego hrs x $15 + 700 driving x $8 + 300
  // drone x $25) x 12 = $1.06M at 6,000 hrs/month; the curve first reaches
  // $1.0M in Mar 2027 (month 0 of the comps chart, level with Mercor's m0).
  // Plateaus at ~$1.5M as volume moves to direct Phase 1 contracts. Net = 1 - payouts (ego $4 of $15; driving ~$2.5 of $8;
  // drone ~$5 of $25) ~ 0.70.
  {
    id: "agency",
    phase: "p0",
    name: "Phase 0 agency contract",
    start: 2026.75,
    cap: 1.5e6,
    mid: AGENCY_MID,
    rate: 8,
    netShare: 0.7,
    ease: 0,
    upside: { times: 1 },
  },
  // Driving 1.0M hrs x $8 + drone 0.3M hrs x $25 = $15.5M. Both prices are
  // assumptions (no public AI-training price; Uber gives AV data free).
  // Upside: $39M by Mar 2030.
  {
    id: "mobility",
    phase: "p1",
    name: "Driving & drone video",
    start: 2027.25,
    cap: 15e6,
    mid: 2029,
    rate: 2,
    netShare: 0.5,
    ease: 1,
    upside: { value: 39e6, at: UPSIDE_AT },
  },
  // Saturation: 125k active collectors across the agency network x 16
  // accepted hrs/mo x 12 = 24M hrs; 83% custom = 20M hrs x $12/hr (raw $15
  // falling 10%/yr, floored at $12) x 1 licence (exclusive) = $240M.
  // Net = 1 - ($3 collector + $1 agency) / $12 ~ 0.67.
  // Upside: 160k collectors x 18 hrs x 12 = 34.6M hrs; 83% custom at $14 with
  // no price decline = $402M by Mar 2030.
  {
    id: "custom",
    phase: "p1",
    name: "Custom collection",
    start: 2027.25,
    cap: 240e6,
    mid: 2029.4,
    rate: 1.8,
    netShare: 0.67,
    ease: 1,
    upside: { value: 402e6, at: UPSIDE_AT },
  },
  // 4M separate own-spec hrs/yr (not the custom hours) x $5/hr x 1.0 licence
  // = $20M. Net = 1 - $4 payout / $5 = 0.20. Upside: 5.5M hrs x $6 x 1.5
  // licences = $50M.
  {
    id: "catalogue",
    phase: "p1",
    name: "Catalogue licences",
    start: 2027.75,
    cap: 20e6,
    mid: 2029.5,
    rate: 1.8,
    netShare: 0.2,
    ease: 1,
    upside: { value: 50e6, at: UPSIDE_AT },
  },
  // 20M custom hrs x ~31% attach x $12/hr add-on = $75M; annotators are paid
  // ~60% of the SKU, so net 0.40. Upside: 40% attach at $15 = $172M.
  {
    id: "annotation",
    phase: "p1",
    name: "Annotation add-on",
    start: 2027.5,
    cap: 75e6,
    mid: 2029.4,
    rate: 1.8,
    netShare: 0.4,
    ease: 1,
    upside: { value: 172e6, at: UPSIDE_AT },
  },
  // Dumuria model farm (under 8 ha) + 5,000 farmer-cultivated instrumented
  // plots x ~22 hrs/mo x 12 ~ 1.33M hrs x $60/hr x 1 licence = $80M, only once
  // the LOI converts. Payouts: collector $3 + farm data fee $5 + agronomist $4
  // = $12, net 0.80. Upside: 2.0M hrs x $80 = $160M.
  {
    id: "premium",
    phase: "p1",
    name: "Sensor-synced farm video",
    start: 2027.75,
    cap: 80e6,
    mid: 2029.8,
    rate: 1.7,
    netShare: 0.8,
    ease: 1,
    upside: { value: 160e6, at: UPSIDE_AT },
  },
  // 5 customers x $5M/yr = $25M at saturation; gated on one paid pilot. Net
  // 0.85 (~15% to agronomist task writers). Upside: 12 customers x $6M = $72M.
  {
    id: "bench",
    phase: "p1",
    name: "AgricultureBench",
    start: 2028,
    cap: 25e6,
    mid: 2029.6,
    rate: 1.7,
    netShare: 0.85,
    ease: 1,
    upside: { value: 72e6, at: UPSIDE_AT },
  },
  // Partner holdings x 40 instrumented hrs/holding/yr x $30/hr (the $60
  // premium halved as partner volume scales) x 1 licence = $1,200 per holding a year.
  // Onboarding is a constant pace, held to the PRAN benchmark (~100k contract
  // farmers over decades): Phase 1 agencies sign ~HOLDINGS_PER_MONTH holdings
  // a month after a six-month ease-in, so ~33k 18 months in (Dec 2030) and
  // ~59k at Dec 2031. Payouts: 30% farm revenue share + ~10% collector + ~5%
  // labelling, net 0.55. Upside: same holdings at $40/hr.
  {
    id: "farmdata",
    phase: "p2",
    name: "Farm data network",
    start: 2029.5,
    cap: 360e6,
    mid: 2032.5,
    rate: 1.6,
    netShare: 0.55,
    ease: 0.5,
    upside: { times: 40 / 30 },
    perYear: HOLDINGS_PER_MONTH * 12 * HOLDING_ARR,
  },
] as const satisfies readonly Stream[];

// Legend groups for the Roadmap, bottom of the stack first; each group's
// streams are adjacent in STREAMS. A group is drawn in its `color` stream's
// fill, so every band on the chart has a legend entry; `sub` names what the
// group folds in.
export const STREAM_GROUPS = [
  {
    name: "Collection",
    sub: "custom · catalogue · driving & drone",
    color: "custom",
    streams: ["agency", "mobility", "custom", "catalogue"],
  },
  { name: "Annotation", sub: "", color: "annotation", streams: ["annotation"] },
  {
    name: "Farm video + AgricultureBench",
    sub: "",
    color: "premium",
    streams: ["premium", "bench"],
  },
  { name: "Farm data network", sub: "", color: "farmdata", streams: ["farmdata"] },
] as const satisfies readonly {
  name: string;
  sub: string;
  color: StreamId;
  streams: readonly StreamId[];
}[];

export const BILLION = 1e9;
// Chart window: Nov 2026 to the end of Dec 2031.
export const ROADMAP_START = 2026 + 10 / 12;
export const HORIZON_END = 2032;
// The first $1M month (month 0 for the comps chart).
export const MONTH_ZERO = 2027.25;

const logistic = (s: Stream, year: number) => 1 / (1 + Math.exp(-s.rate * (year - s.mid)));
const ramp = (s: Stream, year: number) => {
  if (year <= s.start) return 0;
  const l0 = logistic(s, s.start);
  const u = s.ease > 0 ? Math.min(1, (year - s.start) / s.ease) : 1;
  return ((logistic(s, year) - l0) / (1 - l0)) * u * u * (3 - 2 * u);
};
// Years of full-pace onboarding since start: the integral of a smoothstep
// ease-in, so the line leaves zero with zero slope and then runs straight.
const paced = (s: Stream, year: number) => {
  const d = year - s.start;
  if (d <= 0) return 0;
  if (s.ease <= 0 || d >= s.ease) return d - s.ease / 2;
  const u = d / s.ease;
  return s.ease * (u * u * u - (u * u * u * u) / 2);
};

export const streamArr = (s: Stream, year: number) =>
  s.perYear === undefined ? s.cap * ramp(s, year) : Math.min(s.cap, s.perYear * paced(s, year));

// The forecast's stated upside path for Phase 0-1 gross ARR, by months since
// the first $1M month: m0 $1M, m6 $12M, m12 $45M, m18 $150M, m24 $330M,
// m30 $640M, then the Phase 0-1 ceiling at m36 (Mar 2030). Log-linear between
// stated points (so it stays under Mercor's log-linear m0-m10 pace); the last
// six months ease onto the ceiling with zero slope, a cubic in log space.
const UPSIDE_MONTHS = [0, 6, 12, 18, 24, 30, 36] as const;
const UPSIDE_EARLY = [1e6, 12e6, 45e6, 150e6, 330e6, 640e6] as const;
const valued = (s: Stream): s is Stream & { upside: { value: number; at: number } } =>
  "value" in s.upside;
const timesUpside = (s: Stream, year: number) =>
  "times" in s.upside ? s.upside.times * streamArr(s, year) : 0;

const logPath = (xs: readonly number[], ys: readonly number[]) => {
  const n = xs.length;
  return (xv: number) => {
    if (xv <= xs[0]!) return ys[0]!;
    if (xv >= xs[n - 1]!) return ys[n - 1]!;
    let i = 0;
    while (xv > xs[i + 1]!) i++;
    const h = xs[i + 1]! - xs[i]!;
    const t = (xv - xs[i]!) / h;
    if (i < n - 2) return ys[i]! + t * (ys[i + 1]! - ys[i]!);
    // Last segment: Hermite from the previous segment's slope to zero.
    const m0 = (ys[i]! - ys[i - 1]!) / (xs[i]! - xs[i - 1]!);
    const t2 = t * t;
    const t3 = t2 * t;
    return (
      (2 * t3 - 3 * t2 + 1) * ys[i]! + (t3 - 2 * t2 + t) * h * m0 + (-2 * t3 + 3 * t2) * ys[i + 1]!
    );
  };
};

// Phase 0-1 upside ceiling: every stated driver in full at Mar 2030, plus
// the agency contract (upside = plan).
const P01 = () => STREAMS.filter((s) => s.phase === "p0" || s.phase === "p1");
const p01Ceiling = () =>
  P01().reduce((sum, s) => sum + (valued(s) ? s.upside.value : timesUpside(s, UPSIDE_AT)), 0);
let p01Path: ((m: number) => number) | null = null;
const p01Upside = (year: number) => {
  p01Path ??= logPath(
    UPSIDE_MONTHS,
    [...UPSIDE_EARLY, p01Ceiling()].map((v) => Math.log(v)),
  );
  return Math.exp(p01Path((year - MONTH_ZERO) * 12));
};

// A ceiling stream's upside: its share of the Phase 0-1 upside path left
// after the plan-priced streams, weighted by how far along its own ramp it
// is, so a stream that has not started takes nothing and every stream lands
// on its stated value at Mar 2030 and holds there.
export const streamUpside = (s: Stream, year: number): number => {
  if (!valued(s)) return timesUpside(s, year);
  if (year <= MONTH_ZERO) return 0;
  const weight = (t: Stream) =>
    valued(t) ? t.upside.value * Math.min(1, ramp(t, year) / ramp(t, t.upside.at)) : 0;
  const total = P01().reduce((sum, t) => sum + weight(t), 0);
  if (total <= 0) return 0;
  const rest = P01().reduce((sum, t) => sum + timesUpside(t, year), 0);
  return (Math.max(0, p01Upside(year) - rest) * weight(s)) / total;
};

export const planArr = (year: number) => STREAMS.reduce((sum, s) => sum + streamArr(s, year), 0);
export const upsideArr = (year: number) =>
  STREAMS.reduce((sum, s) => sum + streamUpside(s, year), 0);
export const planNet = (year: number) =>
  STREAMS.reduce((sum, s) => sum + s.netShare * streamArr(s, year), 0);
export const phaseArr = (id: PhaseId, year: number) =>
  STREAMS.filter((s) => s.phase === id).reduce((sum, s) => sum + streamArr(s, year), 0);
export const phaseUpside = (id: PhaseId, year: number) =>
  STREAMS.filter((s) => s.phase === id).reduce((sum, s) => sum + streamUpside(s, year), 0);

const MONTH_NAMES = [
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
];
// Label for the month that ends at `year`.
export const monthLabel = (year: number) => {
  const m = Math.round(year * 12) - 1;
  return `${MONTH_NAMES[m % 12]} ${Math.floor(m / 12)}`;
};
// Months since the first $1M month.
export const monthIndex = (year: number) => Math.round((year - MONTH_ZERO) * 12);

// First month-end whose value reaches $1B, or null inside the horizon.
const crossing = (arr: (year: number) => number) => {
  for (let m = Math.round(ROADMAP_START * 12); m <= HORIZON_END * 12; m++) {
    const year = m / 12;
    if (arr(year) >= BILLION) return { year, label: monthLabel(year), month: monthIndex(year) };
  }
  return null;
};
export const PLAN_BILLION = crossing(planArr);
export const UPSIDE_BILLION = crossing(upsideArr);

export type Gate = { year: number; label: string; plan: number; upside: number; net: number };
const gateAt = (year: number): Gate => ({
  year,
  label: monthLabel(year),
  plan: planArr(year),
  upside: upsideArr(year),
  net: planNet(year),
});
// Each phase's exit; Phase 2's is the horizon (Dec 2031).
export const GATES = {
  p0: gateAt(PHASES[0].end),
  p1: gateAt(PHASES[1].end),
  p2: gateAt(PHASES[2].end),
} as const;
export const HORIZON = gateAt(HORIZON_END);
// The Phase 0-1 upside on its ceiling date (Mar 2030): every stated driver
// in full. The forecast's 50k holdings by Mar 2030 would break the <= 35k cap
// on Phase 2's first 18 months, so the upside reaches $1B later than the
// forecast's Mar 2030.
export const UPSIDE_CEILING = {
  ...gateAt(UPSIDE_AT),
  phase1: phaseUpside("p0", UPSIDE_AT) + phaseUpside("p1", UPSIDE_AT),
};

const byId = (id: StreamId) => STREAMS.find((s) => s.id === id)!;

// Phase 1 drivers at its exit: custom hours are 83% of collector hours at
// 16 accepted hrs/collector/month; raw price $15 falling 10%/yr, floored at $12.
const rawPrice = (year: number) => Math.max(12, 15 * 0.9 ** (year - 2027));
export const collectorsAt = (year: number) =>
  streamArr(byId("custom"), year) / (rawPrice(year) * 0.83 * 16 * 12);

// Phase 2 driver: partner holdings implied by the farm data stream.
export const holdingsAt = (year: number) => streamArr(byId("farmdata"), year) / HOLDING_ARR;

// Real-world data spend a year, annualising the VC estimate of $1.5B-50B over
// 2-3 years (Scroll 2026-05-20) over 3 years: low, base (geometric mean) and
// high.
export const DEMAND = [
  { name: "Low", value: 0.5e9 },
  { name: "Base", value: 2.9e9 },
  { name: "High", value: 16.7e9 },
] as const;

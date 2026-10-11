// Roadmap, Nov 2026 to Dec 2028: gross ARR for the two-phase plan. Every
// number below is an assumption to confirm, not an actual. The curves are
// set against Phase 1's end (Dec 2029); the chart stops a year short of it.
//
// Phase 0 (Months 1-5) builds the core product, drone video + evals, on our
// two farms. Phase 1 scales it horizontally: we license access to record
// partner farms and plant sensors there. A "plot" below is one farm's
// recorded land.
//
// Time is a fractional calendar year: 2027.0 is 1 Jan 2027, and a month's
// value is read at its end. Gross ARR is trailing-month revenue x 12.

export type PhaseId = "p0" | "p1";

export const PHASES = [
  { id: "p0", start: 2026 + 10 / 12, end: 2027.25 },
  { id: "p1", start: 2027.25, end: 2030 },
] as const satisfies readonly { id: PhaseId; start: number; end: number }[];

export const ROADMAP_START = PHASES[0].start;
export const ROADMAP_END = 2029;
export const PHASE_0_END = PHASES[0].end;
const PHASE_1_END = PHASES[1].end;

// Tentative dates, each the first of its month, in step with the Month 1-4
// slides (Month 1 is Nov 2026).
// - AgricultureBench 1.0: Month 4, built on the footage of Months 1-3.
// - Sensors: the first non-video sensors go in on our two farms once Phase 0
//   is done, so we learn to install, swap and move sensors before any
//   partner farm depends on it.
// - First partner: the first farm beyond our two sites, a season later, so it
//   joins with a sensor kit that has already been swapped out at least once.
// - AgricultureBench 2.0: evals across partner farms' plots, a season of
//   sensor data behind them; eval sales turn sharply upward after it.
// - Go direct: we sell to AI labs ourselves rather than through brokers.
export const BENCH_1 = 2027 + 1 / 12;
export const SENSORS_START = PHASE_0_END;
export const FIRST_PARTNER = 2027.5;
export const BENCH_2 = 2028;
export const GO_DIRECT = 2028.75;

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
export const monthName = (year: number) =>
  `${MONTHS[Math.round((year % 1) * 12)]} ${Math.floor(year)}`;

// A smooth S-curve from 0 at `start` to 1 at `start + span`.
const ease = (year: number, start: number, span: number) => {
  const u = Math.min(1, Math.max(0, (year - start) / span));
  return u * u * (3 - 2 * u);
};

// Drone video. Each operational plot records 10 hrs a day from Month 3
// (~300 hrs a month), licensed non-exclusively to labs at $25/hr (the deck's drone price,
// unverified). Licenses renew yearly, so the same hours keep earning. Phase 0
// brings our two farms to capacity by its end;
// partner farms then add licensed plots as we learn to onboard them: 1 a
// month from the first partner, 2 four months later, then climbing to 10 a
// month by Apr 2028 and holding there.
//
// Two things lift the value over time:
// - History: each year of continuous record on a plot puts its new footage
//   in more context, so its price rises 25% per year of history.
// - Reuse: the same footage is licensed to more labs, from 2 at the end of
//   Phase 0 to 4 by the end of Phase 1.
const VIDEO = {
  hrsPerPlot: 300,
  rate: 25,
  historyPremium: 0.25,
  buyersPhase0: 2,
  buyersEnd: 4,
  phase0Plots: 2,
  // Partner onboarding pace in plots a month, as (year, rate) points joined
  // by straight lines; flat after the last.
  onboarding: [
    [FIRST_PARTNER, 1],
    [FIRST_PARTNER + 4 / 12, 2],
    [2028.25, 10],
  ] as const,
};
const onboardingRate = (year: number) => {
  const pts = VIDEO.onboarding;
  if (year < pts[0]![0]) return 0;
  for (let i = 0; i < pts.length - 1; i++) {
    const [x0, y0] = pts[i]!;
    const [x1, y1] = pts[i + 1]!;
    if (year < x1) return y0 + ((y1 - y0) * (year - x0)) / (x1 - x0);
  }
  return pts[pts.length - 1]![1];
};
// Partner plots onboarded by `year`: the onboarding rate summed month by
// month (midpoint rule, fine at this resolution).
const partnerPlots = (year: number) => {
  let total = 0;
  const step = 1 / 120;
  for (let t = FIRST_PARTNER; t < year; t += step) {
    const dt = Math.min(step, year - t);
    total += onboardingRate(t + dt / 2) * 12 * dt;
  }
  return total;
};
const plotsAt = (year: number) =>
  VIDEO.phase0Plots * ease(year, PHASES[0].start, PHASE_0_END - PHASES[0].start) +
  partnerPlots(year);
const buyersAt = (year: number) =>
  VIDEO.buyersPhase0 +
  (VIDEO.buyersEnd - VIDEO.buyersPhase0) * ease(year, PHASE_0_END, PHASE_1_END - PHASE_0_END);
// Sum over monthly cohorts of plots: each cohort's footage is priced by how
// long that plot has been on record.
const COHORT = 1 / 12;
const videoArr = (year: number) => {
  let perBuyer = 0;
  for (let s = ROADMAP_START; s < year; s += COHORT) {
    const added = plotsAt(Math.min(s + COHORT, year)) - plotsAt(s);
    const history = year - s;
    perBuyer += added * VIDEO.hrsPerPlot * 12 * VIDEO.rate * (1 + VIDEO.historyPremium * history);
  }
  return perBuyer * buyersAt(year);
};

// Evals (benchmarks and RL environments) on our data, sold to labs at about
// $400k a quarter per contract (Epoch, Jan 2026: $300k-$500k a quarter).
// AgricultureBench 1.0 wins one contract by the end of Phase 0 and 4 by
// AgricultureBench 2.0. 2.0 covers many farms' plots, so contracts then climb
// to 25 over the following 18 months.
const EVALS = {
  perContract: 1.6e6,
  byPhase0End: 1,
  byBench2: 4,
  byEnd: 25,
  bench2Span: 1.5,
};
const contractsAt = (year: number) =>
  EVALS.byPhase0End * ease(year, BENCH_1, PHASE_0_END - BENCH_1) +
  (EVALS.byBench2 - EVALS.byPhase0End) * ease(year, PHASE_0_END, BENCH_2 - PHASE_0_END) +
  (EVALS.byEnd - EVALS.byBench2) * ease(year, BENCH_2, EVALS.bench2Span);
const evalsArr = (year: number) => EVALS.perContract * contractsAt(year);

// Bottom of the stack first.
export const STREAMS = [
  { id: "video", name: "Drone video", color: "custom", arr: videoArr },
  { id: "evals", name: "Evals", color: "premium", arr: evalsArr },
] as const;

// The assumptions as the slide states them, read at the chart's end.
export const ASSUMPTIONS = {
  evalQuarter: EVALS.perContract / 4,
  evalsPhase0: EVALS.byPhase0End,
  evalsEnd: Math.round(contractsAt(ROADMAP_END)),
  endYear: ROADMAP_END - 1,
  plots: VIDEO.phase0Plots,
  hrsPerDay: VIDEO.hrsPerPlot / 30,
  rate: VIDEO.rate,
  historyPremium: VIDEO.historyPremium,
  buyersPhase0: VIDEO.buyersPhase0,
  buyersEnd: Math.round(buyersAt(ROADMAP_END)),
  onboardingStart: VIDEO.onboarding[0][1],
  onboardingEarly: VIDEO.onboarding[1][1],
  onboardingFull: VIDEO.onboarding[2][1],
  onboardingFrom: monthName(VIDEO.onboarding[0][0]),
  onboardingFullFrom: monthName(VIDEO.onboarding[2][0]),
} as const;

export const planArr = (year: number) => STREAMS.reduce((sum, s) => sum + s.arr(year), 0);
export const PLAN_END = planArr(ROADMAP_END);
export const PHASE_0_ARR = planArr(PHASE_0_END);

// Roadmap, Nov 2026 to Dec 2029: gross ARR for the two-phase plan. Every
// number below is an assumption to confirm, not an actual.
//
// Phase 0 (Months 1-5) builds the core product, drone video + evals, on four
// plots. Phase 1 scales it horizontally: we license access to record more
// farms' plots and plant sensors there.
//
// Time is a fractional calendar year: 2027.0 is 1 Jan 2027, and a month's
// value is read at its end. Gross ARR is trailing-month revenue x 12.

export type PhaseId = "p0" | "p1";

export const PHASES = [
  { id: "p0", start: 2026 + 10 / 12, end: 2027.25 },
  { id: "p1", start: 2027.25, end: 2030 },
] as const satisfies readonly { id: PhaseId; start: number; end: number }[];

export const ROADMAP_START = PHASES[0].start;
export const ROADMAP_END = 2030;
export const PHASE_0_END = PHASES[0].end;

// A smooth S-curve from 0 at `start` to 1 at `start + span`.
const ease = (year: number, start: number, span: number) => {
  const u = Math.min(1, Math.max(0, (year - start) / span));
  return u * u * (3 - 2 * u);
};

// Drone video. Each operational plot records 10 hrs a day (~300 hrs a
// month), licensed non-exclusively to labs at $25/hr (the deck's drone price,
// unverified). Licenses renew yearly, so the same hours keep earning. Phase 0
// brings four plots (one family plot, three of ours) to capacity by its end;
// Phase 1 adds licensed plots as we learn to onboard them: 1 a month at
// first, 2 by the end of its fourth month, then climbing to 10 a month by
// the start of 2028 and holding there.
//
// Two things lift the value over time:
// - History: each year of continuous record on a plot puts its new footage
//   in more context, so its price rises 25% per year of history.
// - Reuse: the same footage is licensed to more labs, from 2 at the end of
//   Phase 0 to 4 by Dec 2029.
const VIDEO = {
  hrsPerPlot: 300,
  rate: 25,
  historyPremium: 0.25,
  buyersPhase0: 2,
  buyersEnd: 4,
  phase0Plots: 4,
  // Phase 1 onboarding pace in plots a month, as (year, rate) points joined
  // by straight lines; flat after the last.
  onboarding: [
    [PHASE_0_END, 1],
    [PHASE_0_END + 4 / 12, 2],
    [2028, 10],
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
// Phase 1 plots onboarded by `year`: the onboarding rate summed month by
// month (midpoint rule, fine at this resolution).
const phase1Plots = (year: number) => {
  let total = 0;
  const step = 1 / 120;
  for (let t = PHASE_0_END; t < year; t += step) {
    const dt = Math.min(step, year - t);
    total += onboardingRate(t + dt / 2) * 12 * dt;
  }
  return total;
};
const plotsAt = (year: number) =>
  VIDEO.phase0Plots * ease(year, PHASES[0].start, PHASE_0_END - PHASES[0].start) +
  phase1Plots(year);
const buyersAt = (year: number) =>
  VIDEO.buyersPhase0 +
  (VIDEO.buyersEnd - VIDEO.buyersPhase0) * ease(year, PHASE_0_END, ROADMAP_END - PHASE_0_END);
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
// The first evals ship in Month 3; one contract by the end of Phase 0. Each
// new plot widens what the evals can cover, so contracts grow to 25 by Dec
// 2029 and evals become most of the revenue.
const EVALS = { perContract: 1.6e6, byPhase0End: 1, byEnd: 25 };
const evalsArr = (year: number) =>
  EVALS.perContract *
  (EVALS.byPhase0End * ease(year, 2027, PHASE_0_END - 2027) +
    (EVALS.byEnd - EVALS.byPhase0End) * ease(year, PHASE_0_END, ROADMAP_END - PHASE_0_END));

// Bottom of the stack first.
export const STREAMS = [
  { id: "video", name: "Drone video", color: "custom", arr: videoArr },
  { id: "evals", name: "Evals", color: "premium", arr: evalsArr },
] as const;

// The assumptions as the slide states them.
export const ASSUMPTIONS = {
  evalQuarter: EVALS.perContract / 4,
  evalsPhase0: EVALS.byPhase0End,
  evalsEnd: EVALS.byEnd,
  endYear: ROADMAP_END - 1,
  plots: VIDEO.phase0Plots,
  hrsPerDay: VIDEO.hrsPerPlot / 30,
  rate: VIDEO.rate,
  historyPremium: VIDEO.historyPremium,
  buyersPhase0: VIDEO.buyersPhase0,
  buyersEnd: VIDEO.buyersEnd,
  onboardingStart: VIDEO.onboarding[0][1],
  onboardingEarly: VIDEO.onboarding[1][1],
  onboardingFull: VIDEO.onboarding[2][1],
  onboardingFullFrom: VIDEO.onboarding[2][0],
} as const;

export const planArr = (year: number) => STREAMS.reduce((sum, s) => sum + s.arr(year), 0);
export const PLAN_END = planArr(ROADMAP_END);
export const PHASE_0_ARR = planArr(PHASE_0_END);

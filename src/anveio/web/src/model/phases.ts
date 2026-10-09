// Path to $1B ARR and beyond: an illustrative top-down build, separate from
// the bottom-up Phase 1 operating model in model.ts. Every number here is an
// assumption to be replaced as each phase produces real data.
//
// Each revenue stream ramps on its own logistic S-curve from the date it
// starts earning; total ARR is their sum. A phase is the period that opens
// when its first stream starts.

export type PhaseId = "collect" | "sense" | "make" | "power";

export type Phase = {
  readonly id: PhaseId;
  readonly number: number;
  readonly name: string;
  // Fractional calendar year the phase opens (2028.0 = Jan 2028).
  readonly start: number;
  // Group EBITDA margin when this phase opens (the gate); the first phase
  // has no gate.
  readonly gateMargin: number | null;
};

export const PHASES = [
  { id: "collect", number: 1, name: "Collect", start: 2026 + 10 / 12, gateMargin: null },
  { id: "sense", number: 2, name: "Sense", start: 2027.75, gateMargin: 0 },
  { id: "make", number: 3, name: "Make", start: 2029.25, gateMargin: 0.2 },
  { id: "power", number: 4, name: "Power", start: 2030.0, gateMargin: 0.22 },
] as const satisfies readonly Phase[];

export type StreamId =
  | "driving"
  | "egocentric"
  | "drone"
  | "sensor"
  | "materials"
  | "medicine"
  | "energy";

export type Stream = {
  readonly id: StreamId;
  readonly phase: PhaseId;
  readonly name: string;
  // Fractional calendar year the stream starts earning; defaults to its
  // phase's start.
  readonly start?: number;
  // Stream ARR at saturation.
  readonly cap: number;
  // Year the stream reaches half its cap.
  readonly mid: number;
  // Logistic steepness, per year.
  readonly rate: number;
};

// Bottom of the stack first.
export const STREAMS = [
  { id: "driving", phase: "collect", name: "Driving video", cap: 120e6, mid: 2027.9, rate: 3.4 },
  {
    id: "egocentric",
    phase: "collect",
    name: "Egocentric video",
    cap: 420e6,
    mid: 2027.9,
    rate: 3.4,
  },
  {
    id: "drone",
    phase: "collect",
    name: "Drone video",
    start: 2027.1,
    cap: 150e6,
    mid: 2028.0,
    rate: 3.4,
  },
  {
    id: "sensor",
    phase: "sense",
    name: "Agricultural sensor data",
    cap: 600e6,
    mid: 2028.6,
    rate: 3.0,
  },
  {
    id: "materials",
    phase: "make",
    name: "Building materials",
    cap: 900e6,
    mid: 2030.4,
    rate: 2.6,
  },
  {
    id: "medicine",
    phase: "make",
    name: "Medicine",
    start: 2029.5,
    cap: 700e6,
    mid: 2030.6,
    rate: 2.6,
  },
  { id: "energy", phase: "power", name: "Energy", cap: 1.2e9, mid: 2031.1, rate: 2.4 },
] as const satisfies readonly Stream[];

export const PATH_TARGET = 1e9;
// EBITDA margin at the target.
export const TARGET_MARGIN = 0.2;
// The path is drawn to the start of this year.
export const PATH_END = 2031;

const phaseOf = (id: PhaseId) => PHASES.find((p) => p.id === id)!;
const startOf = (s: Stream) => s.start ?? phaseOf(s.phase).start;
const logistic = (s: Stream, year: number) => s.cap / (1 + Math.exp(-s.rate * (year - s.mid)));

// Zero before the stream starts, then the S-curve shifted down so it rises
// from zero instead of jumping.
export const streamArr = (s: Stream, year: number) => {
  const start = startOf(s);
  return year <= start ? 0 : Math.max(0, logistic(s, year) - logistic(s, start));
};

export const phaseArr = (id: PhaseId, year: number) =>
  STREAMS.filter((s) => s.phase === id).reduce((sum, s) => sum + streamArr(s, year), 0);

export const totalArr = (year: number) => STREAMS.reduce((sum, s) => sum + streamArr(s, year), 0);

// First month total ARR reaches the target.
export const targetYear = (() => {
  for (let year = PHASES[0].start; year < PATH_END; year += 1 / 12) {
    if (totalArr(year) >= PATH_TARGET) return year;
  }
  throw new Error("path never reaches the ARR target");
})();

// Each phase's opening, for its header on the chart.
export const phaseGate = (p: Phase) => ({
  year: p.start,
  arr: totalArr(p.start),
  margin: p.gateMargin,
});

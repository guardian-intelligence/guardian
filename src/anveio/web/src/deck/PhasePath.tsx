import {
  BENCH_1,
  BENCH_2,
  FIRST_PARTNER,
  GO_DIRECT,
  PHASES,
  PLAN_END,
  planArr,
  ROADMAP_END,
  ROADMAP_START,
  SENSORS_START,
  STREAMS,
} from "~/model/roadmap";
import { money } from "./format";
import { Brace } from "./ValueChain";

// The roadmap: gross ARR stacked by revenue stream, each phase a tinted band.
// The only words on the plot are its milestones, each a pointer to its point.

const W = 1180;
const H = 290;
// The right margin holds each stream's brace and its ARR at the chart's end.
const m = { left: 52, right: 150, top: 14, bottom: H - 26 };
const X0 = ROADMAP_START;
const X1 = ROADMAP_END;
const STEPS = 26 * 8;
const YEARS = Array.from({ length: STEPS + 1 }, (_, i) => X0 + ((X1 - X0) * i) / STEPS);
// A round top just above the plan's end.
// The smallest round step that keeps the axis to six ticks or fewer.
const TICK = [5e6, 10e6, 20e6, 25e6, 50e6, 100e6].find((t) => PLAN_END / t <= 5)!;
const Y1 = Math.ceil(PLAN_END / TICK) * TICK;
const Y_TICKS = Array.from({ length: Y1 / TICK + 1 }, (_, i) => i * TICK);

const x = (year: number) => m.left + ((W - m.left - m.right) * (year - X0)) / (X1 - X0);
const y = (v: number) => m.bottom - ((m.bottom - m.top) * v) / Y1;
// "Q3 ’27" for the quarter holding the month that starts at `year`.
const quarter = (year: number) => {
  const month = Math.round((year % 1) * 12);
  return `Q${Math.floor(month / 3) + 1} ’${String(Math.floor(year) % 100).padStart(2, "0")}`;
};
const path = (pts: readonly (readonly [number, number])[]) =>
  pts.map(([px, py], k) => `${k ? "L" : "M"}${px.toFixed(1)},${py.toFixed(1)}`).join(" ");

// Cumulative stack: TOPS[i][j] is the top of stream i at YEARS[j].
const TOPS = STREAMS.reduce<number[][]>((acc, s) => {
  const below = acc[acc.length - 1];
  return [...acc, YEARS.map((yr, j) => (below?.[j] ?? 0) + s.arr(yr))];
}, []);

// Each stream's share at the chart's end: a brace over its band, then its
// ARR and name.
const ENDS = STREAMS.map((s, i) => {
  const j = YEARS.length - 1;
  return {
    stream: s,
    top: y(TOPS[i]![j]!) + 2,
    bottom: y(TOPS[i - 1]?.[j] ?? 0) - 2,
    value: s.arr(X1),
  };
});

const AREAS = STREAMS.map((s, i) => {
  const top = YEARS.map((yr, j) => [x(yr), y(TOPS[i]![j]!)] as const);
  const bottom = YEARS.map((yr, j) => [x(yr), y(TOPS[i - 1]?.[j] ?? 0)] as const).reverse();
  return { stream: s, d: `${path([...top, ...bottom])} Z` };
});

// Milestones: a dot on the curve, a leader to its label, and its quarter on
// the x axis. Labels on the flat start step down a row each, so each clears
// the next one's leader; the last, high on the curve, hangs to the left.
const MILESTONES = (
  [
    { id: "bench-1", year: BENCH_1, label: "AgricultureBench 1.0", dx: 24, dy: -170 },
    { id: "sensors", year: SENSORS_START, label: "First sensor installed", dx: 24, dy: -125 },
    { id: "partner", year: FIRST_PARTNER, label: "First partner farm", dx: 24, dy: -80 },
    { id: "bench-2", year: BENCH_2, label: "AgricultureBench 2.0", dx: 24, dy: -80 },
    { id: "direct", year: GO_DIRECT, label: "Go direct to AI labs", dx: -24, dy: -50 },
  ] as const
).map((ms) => ({ ...ms, at: { x: x(ms.year), y: y(planArr(ms.year)) } }));
// The x axis names only the milestones' quarters, and the chart's last
// quarter unless a milestone already names it.
const LAST = { x: x(X1), label: quarter(X1 - 1 / 12), anchor: "end" } as const;
const X_TICKS = [
  ...MILESTONES.map((ms) => ({ x: ms.at.x, label: quarter(ms.year), anchor: "middle" }) as const),
  ...(MILESTONES.some((ms) => quarter(ms.year) === LAST.label) ? [] : [LAST]),
];

export function PhasePath() {
  return (
    <figure className="chart phase-path">
      {/* Centred over the plot, not the margins. */}
      <figcaption
        className="growth-heading phase-path-title"
        style={{ padding: `0 ${(100 * m.right) / W}% 0 ${(100 * m.left) / W}%` }}
      >
        Projected Gross ARR (Phase 1, Data)
      </figcaption>
      <svg
        viewBox={`0 0 ${W} ${H}`}
        role="img"
        aria-label={`Gross ARR, Nov 2026 to Dec 2028, ${money(PLAN_END)} at the end. Milestones: ${MILESTONES.map((ms) => `${ms.label} ${quarter(ms.year)}`).join(", ")}.`}
      >
        {PHASES.map((p) => (
          <rect
            key={p.id}
            x={x(p.start)}
            y={m.top}
            width={x(Math.min(p.end, X1)) - x(p.start)}
            height={m.bottom - m.top}
            className="phase-band"
            style={{ fill: `var(--phase-${p.id})` }}
          />
        ))}
        {Y_TICKS.map((v) => (
          <g key={v}>
            <line x1={m.left} x2={x(X1)} y1={y(v)} y2={y(v)} className="grid" />
            <text
              x={m.left - 10}
              y={y(v)}
              className="tick"
              textAnchor="end"
              dominantBaseline="middle"
            >
              {v ? money(v) : "$0"}
            </text>
          </g>
        ))}
        {AREAS.map((a) => (
          <path key={a.stream.id} d={a.d} style={{ fill: `var(--stream-${a.stream.color})` }} />
        ))}
        {ENDS.map((e) => {
          const mid = (e.top + e.bottom) / 2;
          const tx = x(X1) + 40;
          return (
            <g key={e.stream.id}>
              <Brace x={x(X1) + 8} top={e.top} bottom={e.bottom} />
              <text x={tx} y={mid - 9} className="end-value" dominantBaseline="middle">
                {money(e.value)}
              </text>
              <rect
                x={tx}
                y={mid + 11 - 5.5}
                width={11}
                height={11}
                rx={2}
                style={{ fill: `var(--stream-${e.stream.color})` }}
              />
              <text x={tx + 17} y={mid + 11} className="end-name" dominantBaseline="middle">
                {e.stream.name}
              </text>
            </g>
          );
        })}
        <line x1={m.left} x2={x(X1)} y1={m.bottom} y2={m.bottom} className="axis" />
        {X_TICKS.map((t) => (
          <g key={t.label}>
            <line x1={t.x} x2={t.x} y1={m.bottom} y2={m.bottom + 5} className="axis" />
            <text x={t.x} y={m.bottom + 19} className="tick" textAnchor={t.anchor}>
              {t.label}
            </text>
          </g>
        ))}
        {MILESTONES.map((ms) => {
          const end = { x: ms.at.x + ms.dx, y: ms.at.y + ms.dy };
          return (
            <g key={ms.id}>
              <path
                d={`M${ms.at.x},${ms.at.y - 6} L${end.x},${end.y}`}
                className="milestone-leader"
              />
              <circle cx={ms.at.x} cy={ms.at.y} r={4.5} className="gate-dot" />
              <text
                x={end.x + Math.sign(ms.dx) * 6}
                y={end.y}
                className="milestone-label"
                textAnchor={ms.dx < 0 ? "end" : "start"}
                dominantBaseline="middle"
              >
                {ms.label}
              </text>
            </g>
          );
        })}
      </svg>
    </figure>
  );
}

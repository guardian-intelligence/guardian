import {
  PHASE_0_ARR,
  PHASE_0_END,
  PHASES,
  PLAN_END,
  ROADMAP_END,
  ROADMAP_START,
  STREAMS,
} from "~/model/roadmap";
import { money } from "./format";

// The roadmap: gross ARR stacked by revenue stream, each phase a tinted band.
// The only words on the plot are its milestones, each a pointer to its point.

const W = 780;
const H = 330;
const m = { left: 52, right: 24, top: 14, bottom: H - 26 };
const X0 = ROADMAP_START;
const X1 = ROADMAP_END;
const STEPS = 62 * 4;
const YEARS = Array.from({ length: STEPS + 1 }, (_, i) => X0 + ((X1 - X0) * i) / STEPS);
// A round top just above the plan's end.
// The smallest round step that keeps the axis to six ticks or fewer.
const TICK = [5e6, 10e6, 20e6, 25e6, 50e6, 100e6].find((t) => PLAN_END / t <= 5)!;
const Y1 = Math.ceil(PLAN_END / TICK) * TICK;
const Y_TICKS = Array.from({ length: Y1 / TICK + 1 }, (_, i) => i * TICK);
// Year starts; the axis ends at the start of 2030.
const X_TICKS = [2027, 2028, 2029, 2030];

const x = (year: number) => m.left + ((W - m.left - m.right) * (year - X0)) / (X1 - X0);
const y = (v: number) => m.bottom - ((m.bottom - m.top) * v) / Y1;
const path = (pts: readonly (readonly [number, number])[]) =>
  pts.map(([px, py], k) => `${k ? "L" : "M"}${px.toFixed(1)},${py.toFixed(1)}`).join(" ");

// Cumulative stack: TOPS[i][j] is the top of stream i at YEARS[j].
const TOPS = STREAMS.reduce<number[][]>((acc, s) => {
  const below = acc[acc.length - 1];
  return [...acc, YEARS.map((yr, j) => (below?.[j] ?? 0) + s.arr(yr))];
}, []);

const AREAS = STREAMS.map((s, i) => {
  const top = YEARS.map((yr, j) => [x(yr), y(TOPS[i]![j]!)] as const);
  const bottom = YEARS.map((yr, j) => [x(yr), y(TOPS[i - 1]?.[j] ?? 0)] as const).reverse();
  return { stream: s, d: `${path([...top, ...bottom])} Z` };
});

// Milestones: a dot on the curve and a short leader up and to the right to
// its label.
const MILESTONES = [
  {
    id: "p0",
    at: { x: x(PHASE_0_END), y: y(PHASE_0_ARR) },
    label: `End of Phase 0: ${money(PHASE_0_ARR)} ARR`,
  },
] as const;
const LEADER = { dx: 30, dy: -120 };

export function PhasePath() {
  return (
    <figure className="chart phase-path">
      <ul className="phase-path-legend">
        {STREAMS.map((s) => (
          <li key={s.id}>
            <span style={{ background: `var(--stream-${s.color})` }} />
            {s.name}
          </li>
        ))}
      </ul>
      <svg
        viewBox={`0 0 ${W} ${H}`}
        role="img"
        aria-label={`Gross ARR, Nov 2026 to Dec 2029: ${money(PHASE_0_ARR)} at the end of Phase 0, ${money(PLAN_END)} by Dec 2029.`}
      >
        {PHASES.map((p) => (
          <rect
            key={p.id}
            x={x(p.start)}
            y={m.top}
            width={x(p.end) - x(p.start)}
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
        <line x1={m.left} x2={x(X1)} y1={m.bottom} y2={m.bottom} className="axis" />
        {X_TICKS.map((yr) => (
          <text key={yr} x={x(yr)} y={m.bottom + 18} className="tick" textAnchor="middle">
            {yr}
          </text>
        ))}
        {MILESTONES.map((ms) => {
          const end = { x: ms.at.x + LEADER.dx, y: ms.at.y + LEADER.dy };
          return (
            <g key={ms.id}>
              <path
                d={`M${ms.at.x},${ms.at.y - 6} L${end.x},${end.y}`}
                className="milestone-leader"
              />
              <circle cx={ms.at.x} cy={ms.at.y} r={4.5} className="gate-dot" />
              <text x={end.x + 6} y={end.y} className="milestone-label" dominantBaseline="middle">
                {ms.label}
              </text>
            </g>
          );
        })}
      </svg>
    </figure>
  );
}

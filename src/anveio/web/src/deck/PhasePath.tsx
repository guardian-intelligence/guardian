import {
  PATH_END,
  PATH_TARGET,
  PHASES,
  STREAMS,
  TARGET_MARGIN,
  phaseGate,
  streamArr,
  targetYear,
} from "~/model/phases";
import { money } from "./format";

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const monthYear = (year: number) => {
  const months = Math.round(year * 12);
  return `${MONTHS[months % 12]} ${Math.floor(months / 12)}`;
};
const pct = (v: number) => `${Math.round(v * 100)}%`;
const marginText = (m: number) => (m === 0 ? "EBITDA breakeven" : `${pct(m)} EBITDA`);

const W = 1120;
const H = 400;
// Phase headers sit above the plot; stream labels to its right.
const m = { left: 56, right: 170, top: 96, bottom: H - 28 };
// Height of the phase header strip above the plot.
const HEAD = 90;
const X0 = PHASES[0].start;
const X1 = PATH_END;
const STEPS = 240;
const YEARS = Array.from({ length: STEPS + 1 }, (_, i) => X0 + ((X1 - X0) * i) / STEPS);

// Cumulative stack: TOPS[i][j] is the top of stream i at YEARS[j].
const TOPS = STREAMS.reduce<number[][]>((acc, s) => {
  const below = acc[acc.length - 1];
  return [...acc, YEARS.map((yr, j) => (below?.[j] ?? 0) + streamArr(s, yr))];
}, []);
const PEAK = Math.max(...TOPS[TOPS.length - 1]!);
const Y1 = Math.ceil(PEAK / 1e9) * 1e9;

const x = (year: number) => m.left + ((W - m.left - m.right) * (year - X0)) / (X1 - X0);
const y = (v: number) => m.bottom - ((m.bottom - m.top) * v) / Y1;

const AREAS = STREAMS.map((s, i) => {
  const top = YEARS.map((yr, j) => [x(yr), y(TOPS[i]![j]!)] as const);
  const bottom = YEARS.map((yr, j) => [x(yr), y(TOPS[i - 1]?.[j] ?? 0)] as const).reverse();
  const d = [...top, ...bottom]
    .map(([px, py], k) => `${k ? "L" : "M"}${px.toFixed(1)},${py.toFixed(1)}`)
    .join(" ");
  return { stream: s, d: `${d} Z` };
});

// Stream labels at the right edge, centred on each band and pushed apart
// so thin bands don't overprint.
const LABEL_GAP = 17;
const LABELS = (() => {
  const last = YEARS.length - 1;
  const wanted = STREAMS.map((s, i) => ({
    stream: s,
    y: (y(TOPS[i]![last]!) + y(TOPS[i - 1]?.[last] ?? 0)) / 2,
  }));
  // Bottom of the stack first, so walk upward.
  for (let i = 1; i < wanted.length; i++) {
    wanted[i]!.y = Math.min(wanted[i]!.y, wanted[i - 1]!.y - LABEL_GAP);
  }
  return wanted;
})();

const BANDS = PHASES.map((p, i) => ({ phase: p, from: p.start, to: PHASES[i + 1]?.start ?? X1 }));

const Y_TICKS = Array.from({ length: Y1 / 1e9 + 1 }, (_, i) => i * 1e9);
const X_TICKS = Array.from(
  { length: Math.floor(X1) - Math.ceil(X0) + 1 },
  (_, i) => Math.ceil(X0) + i,
);

const TARGET = { x: x(targetYear), y: y(PATH_TARGET) };

export function PhasePath() {
  return (
    <figure className="chart phase-path">
      <svg
        viewBox={`0 0 ${W} ${H}`}
        role="img"
        aria-label={`ARR by revenue stream, reaching ${money(PATH_TARGET)} in ${monthYear(targetYear)}. ${PHASES.map(
          (p) => {
            const g = phaseGate(p);
            return `Phase ${p.number}, ${p.name}, opens ${monthYear(g.year)} at ${money(g.arr)} ARR`;
          },
        ).join("; ")}`}
      >
        {BANDS.map((b) => (
          <rect
            key={b.phase.id}
            x={x(b.from)}
            y={m.top - HEAD}
            width={x(b.to) - x(b.from)}
            height={m.bottom - m.top + HEAD}
            className="phase-band"
            style={{ fill: `var(--phase-${b.phase.id})` }}
          />
        ))}
        {Y_TICKS.map((v) => (
          <g key={v}>
            <line x1={m.left} x2={W - m.right} y1={y(v)} y2={y(v)} className="grid" />
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
          <path key={a.stream.id} d={a.d} style={{ fill: `var(--stream-${a.stream.id})` }} />
        ))}
        <line x1={m.left} x2={W - m.right} y1={m.bottom} y2={m.bottom} className="axis" />
        {X_TICKS.map((yr) => (
          <text key={yr} x={x(yr)} y={m.bottom + 20} className="tick" textAnchor="middle">
            {yr}
          </text>
        ))}
        {BANDS.map((b) => {
          const g = phaseGate(b.phase);
          const tx = x(b.from) + 10;
          return (
            <g key={b.phase.id}>
              <text x={tx} y={m.top - 68} className="gate-label">
                {`Phase ${b.phase.number} · ${b.phase.name}`}
              </text>
              <text x={tx} y={m.top - 50} className="gate-date">
                {monthYear(g.year)}
              </text>
              {g.margin !== null && (
                <>
                  <text x={tx} y={m.top - 32} className="gate-figure">
                    {`${money(g.arr)} ARR`}
                  </text>
                  <text x={tx} y={m.top - 14} className="gate-figure">
                    {marginText(g.margin)}
                  </text>
                </>
              )}
            </g>
          );
        })}
        <line x1={m.left} x2={TARGET.x} y1={TARGET.y} y2={TARGET.y} className="target-rule" />
        <circle cx={TARGET.x} cy={TARGET.y} r={5} className="gate-dot" />
        <text x={TARGET.x - 10} y={TARGET.y - 12} className="target-label" textAnchor="end">
          {`${money(PATH_TARGET)} ARR · ${monthYear(targetYear)} · ${marginText(TARGET_MARGIN)}`}
        </text>
        {LABELS.map((l) => (
          <text
            key={l.stream.id}
            x={W - m.right + 10}
            y={l.y}
            className="stream-label"
            dominantBaseline="middle"
          >
            <tspan style={{ fill: `var(--stream-${l.stream.id})` }}>■ </tspan>
            {l.stream.name}
          </text>
        ))}
      </svg>
    </figure>
  );
}

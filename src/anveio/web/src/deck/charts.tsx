import { money } from "./format";

export type Bar = { label: string; value: number; note?: string };

export function BarChart({ bars, label }: { bars: readonly Bar[]; label: string }) {
  const W = 960;
  const rowH = 64;
  const m = { left: 240, right: 140 };
  const H = rowH * bars.length;
  const max = Math.max(...bars.map((b) => b.value));
  const w = (v: number) => ((W - m.left - m.right) * v) / max;

  return (
    <figure className="chart">
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={label}>
        {bars.map((b, i) => {
          const cy = rowH * i + rowH / 2;
          return (
            <g key={b.label}>
              <text
                x={m.left - 16}
                y={cy}
                className="bar-label"
                textAnchor="end"
                dominantBaseline="middle"
              >
                {b.label}
              </text>
              <rect x={m.left} y={cy - 16} width={w(b.value)} height={32} rx={3} className="bar" />
              <text x={m.left + w(b.value) + 12} y={cy - 3} className="bar-value">
                {money(b.value)}
              </text>
              {b.note && (
                <text x={m.left + w(b.value) + 12} y={cy + 15} className="end-note">
                  {b.note}
                </text>
              )}
            </g>
          );
        })}
      </svg>
    </figure>
  );
}

import { ARR_TARGET, type NorthStar } from "~/model/model";
import { money } from "./format";

const hours = (h: number) =>
  h >= 999_500 ? `${Number((h / 1_000_000).toPrecision(3))}M` : `${Math.round(h / 1_000)}k`;

// A low-fidelity scaling curve: axis names and one computed tick at the end
// of each axis.
function ScalingCurve({
  title,
  xMax,
  yAt,
  yMax,
  xLabel,
  yLabel,
  xEnd,
  yEnd,
}: {
  title: string;
  xMax: number;
  yAt: (x: number) => number;
  yMax: number;
  xLabel: string;
  yLabel: string;
  xEnd: string;
  yEnd: string;
}) {
  // 4:3 overall, with a 4:3 plot area inside it.
  const H = 214;
  const W = Math.round((H * 4) / 3);
  const m = { left: 44, right: 22, top: 10, bottom: H - 26 };
  const x = (v: number) => m.left + ((W - m.left - m.right) * v) / xMax;
  const y = (v: number) => m.bottom - ((m.bottom - m.top) * v) / yMax;
  const steps = 120;
  const points = Array.from({ length: steps + 1 }, (_, i) => (xMax * i) / steps).map(
    (v) => [x(v), y(yAt(v))] as const,
  );
  const line = points
    .map(([px, py], i) => `${i ? "L" : "M"}${px.toFixed(1)},${py.toFixed(1)}`)
    .join(" ");
  const area = `${line} L${x(xMax)},${y(0)} L${x(0)},${y(0)} Z`;
  const midY = (m.top + m.bottom) / 2;
  const yTitleX = m.left - 16;

  return (
    <figure className="chart chart-square" style={{ width: W }}>
      <figcaption className="chart-title" style={{ paddingLeft: m.left, paddingRight: m.right }}>
        {title}
      </figcaption>
      <svg
        viewBox={`0 0 ${W} ${H}`}
        role="img"
        aria-label={`${yLabel} rising with ${xLabel} to ${yEnd} at ${xEnd}`}
      >
        <line x1={x(xMax)} x2={x(xMax)} y1={y(yMax)} y2={m.bottom} className="guide" />
        <line x1={m.left} x2={x(xMax)} y1={y(yMax)} y2={y(yMax)} className="guide" />
        <path d={area} className="curve-area" />
        <path d={line} className="curve-line" />
        <path
          d={`M${m.left},${m.top - 8} V${m.bottom} H${W - m.right}`}
          className="axis"
          fill="none"
        />
        <line x1={x(xMax)} x2={x(xMax)} y1={m.bottom} y2={m.bottom + 6} className="axis" />
        <line x1={m.left - 6} x2={m.left} y1={y(yMax)} y2={y(yMax)} className="axis" />
        <circle cx={x(xMax)} cy={y(yMax)} r={4} className="curve-dot" />
        <text x={x(xMax)} y={m.bottom + 18} className="tick" textAnchor="middle">
          {xEnd}
        </text>
        <text
          x={m.left - 10}
          y={y(yMax)}
          className="tick"
          textAnchor="end"
          dominantBaseline="middle"
        >
          {yEnd}
        </text>
        <text
          x={(m.left + W - m.right) / 2}
          y={m.bottom + 20}
          className="axis-title"
          textAnchor="middle"
        >
          {xLabel}
        </text>
        <text
          x={yTitleX}
          y={midY}
          className="axis-title"
          textAnchor="middle"
          transform={`rotate(-90 ${yTitleX} ${midY})`}
        >
          {yLabel}
        </text>
      </svg>
    </figure>
  );
}

export function ScalingLaws({ star }: { star: NorthStar }) {
  return (
    <div className="scaling">
      <ScalingCurve
        title="ARR vs. Recorded Hours"
        xMax={star.targetHours}
        yAt={star.arrAt}
        yMax={ARR_TARGET}
        xLabel="Recorded hours"
        yLabel="ARR"
        xEnd={hours(star.targetHours)}
        yEnd={money(ARR_TARGET)}
      />
      <ScalingCurve
        title="Recorded Hours vs. Headcount"
        xMax={star.targetContractors}
        yAt={star.hoursAt}
        yMax={star.targetHours}
        xLabel="Headcount"
        yLabel="Recorded hours"
        xEnd={Math.round(star.targetContractors).toLocaleString("en-US")}
        yEnd={hours(star.targetHours)}
      />
    </div>
  );
}

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

export type HourlyMargin = { label: string; cost: number; price: number };

// Per data type, an all-in cost column and a sale price column growing up
// from a shared baseline, with an arrow from cost to price carrying the markup.
export function MarginChart({ rows }: { rows: readonly HourlyMargin[] }) {
  const W = 600;
  const H = 300;
  const m = { left: 8, right: 8, top: 44, bottom: 264 };
  const max = Math.max(...rows.map((r) => r.price));
  const y = (v: number) => m.bottom - ((m.bottom - m.top) * v) / max;
  const group = (W - m.left - m.right) / rows.length;
  const barW = 56;
  const pair = 64; // distance between the two columns' left edges
  const perHour = (v: number) => `$${v}/hr`;

  return (
    <figure className="chart">
      <svg
        viewBox={`0 0 ${W} ${H}`}
        role="img"
        aria-label={rows
          .map((r) => `${r.label}: all-in cost ${perHour(r.cost)}, sells for ${perHour(r.price)}`)
          .join("; ")}
      >
        <defs>
          <marker
            id="margin-arrow"
            viewBox="0 0 10 10"
            refX="9"
            refY="5"
            markerWidth="5"
            markerHeight="5"
            orient="auto-start-reverse"
          >
            <path d="M0,0 L10,5 L0,10 Z" className="margin-arrowhead" />
          </marker>
        </defs>
        <g className="margin-legend">
          <rect x={m.left} y={4} width={10} height={10} rx={2} className="bar-cost" />
          <text x={m.left + 16} y={13}>
            All-in cost
          </text>
          <rect x={m.left + 104} y={4} width={10} height={10} rx={2} className="bar" />
          <text x={m.left + 120} y={13}>
            Sale price
          </text>
        </g>
        <line x1={m.left} x2={W - m.right} y1={m.bottom} y2={m.bottom} className="axis" />
        {rows.map((r, i) => {
          const center = m.left + group * i + group / 2;
          const costX = center - pair / 2 - barW / 2;
          const priceX = costX + pair;
          const costTop = y(r.cost);
          const priceTop = y(r.price);
          // A quiet curve from above the cost label into the price label's
          // left edge; the multiplier sits just above its tip.
          const from = { x: costX + barW / 2, y: costTop - 30 };
          const to = { x: priceX - 4, y: priceTop - 13 };
          const ctrl = { x: from.x, y: to.y };
          return (
            <g key={r.label}>
              <rect
                x={costX}
                y={costTop}
                width={barW}
                height={m.bottom - costTop}
                rx={3}
                className="bar-cost"
              />
              <rect
                x={priceX}
                y={priceTop}
                width={barW}
                height={m.bottom - priceTop}
                rx={3}
                className="bar"
              />
              <text
                x={costX + barW / 2}
                y={costTop - 8}
                className="margin-cost"
                textAnchor="middle"
              >
                {perHour(r.cost)}
              </text>
              <text
                x={priceX + barW / 2}
                y={priceTop - 8}
                className="margin-price"
                textAnchor="middle"
              >
                {perHour(r.price)}
              </text>
              <path
                d={`M${from.x},${from.y} Q${ctrl.x},${ctrl.y} ${to.x},${to.y}`}
                className="margin-arrow"
                markerEnd="url(#margin-arrow)"
              />
              <text x={to.x - 2} y={to.y - 9} className="margin-markup" textAnchor="end">
                {`${Number((r.price / r.cost).toFixed(1))}×`}
              </text>
              <text x={center} y={m.bottom + 24} className="bar-label" textAnchor="middle">
                {r.label}
              </text>
            </g>
          );
        })}
      </svg>
    </figure>
  );
}

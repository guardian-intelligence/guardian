// Drone footage, per accepted hour: what it costs us all-in beside what it
// could sell for, drawn as a plain journal-style bar figure (title, labelled
// axes with outward ticks, left and bottom spines only).
const COST = 8;
const PRICE = 25;
const TICKS = [0, 10, 20] as const;
// The y axis runs from 0 to the price, its spine ending level with the
// price bar's top.
const MAX = PRICE;

const W = 230;
const H = 210;
const PLOT = { left: 56, right: 214, top: 18, bottom: 168 };
const BAR = 46;
const y = (v: number) => PLOT.bottom - ((PLOT.bottom - PLOT.top) * v) / MAX;
const SLOT = (PLOT.right - PLOT.left) / 2;
const BARS = [
  { label: "Cost", value: COST, className: "drone-cost" },
  { label: "Price", value: PRICE, className: "drone-price" },
] as const;

// Placed inside a parent SVG: (x, y) is the figure's top-left corner, w its
// width; the height follows the figure's aspect.
export const DRONE_MARGIN_ASPECT = H / W;
// The plot's vertical middle as a fraction of the figure's height, so a
// parent can line the bars up with its other elements.
export const DRONE_MARGIN_PLOT_MID = (PLOT.top + PLOT.bottom) / 2 / H;

export function DroneMargin({ x, y: top, w }: { x: number; y: number; w: number }) {
  return (
    <g className="drone-margin">
      <svg
        x={x}
        y={top}
        width={w}
        height={w * DRONE_MARGIN_ASPECT}
        viewBox={`0 0 ${W} ${H}`}
        role="img"
        aria-label={`Cost vs price of drone footage: cost $${COST}, price $${PRICE}.`}
      >
        {TICKS.map((t) => (
          <g key={t}>
            <line x1={PLOT.left - 4} x2={PLOT.left} y1={y(t)} y2={y(t)} className="drone-axis" />
            <text
              x={PLOT.left - 7}
              y={y(t)}
              textAnchor="end"
              dominantBaseline="middle"
              className="drone-tick"
            >
              {t}
            </text>
          </g>
        ))}
        {BARS.map((b, i) => {
          const cx = PLOT.left + SLOT * (i + 0.5);
          return (
            <g key={b.label}>
              <rect
                x={cx - BAR / 2}
                y={y(b.value)}
                width={BAR}
                height={PLOT.bottom - y(b.value)}
                className={b.className}
              />
              <text x={cx} y={y(b.value) - 5} textAnchor="middle" className="drone-tick">
                {`$${b.value}`}
              </text>
              <line x1={cx} x2={cx} y1={PLOT.bottom} y2={PLOT.bottom + 4} className="drone-axis" />
              <text x={cx} y={PLOT.bottom + 17} textAnchor="middle" className="drone-tick">
                {b.label}
              </text>
            </g>
          );
        })}
        <path d={`M${PLOT.left},${y(MAX)} V${PLOT.bottom} H${PLOT.right}`} className="drone-axis" />
        <text
          x={22}
          y={(PLOT.top + PLOT.bottom) / 2}
          textAnchor="middle"
          dominantBaseline="middle"
          className="drone-axis-label"
        >
          $
        </text>
        <text
          x={(PLOT.left + PLOT.right) / 2}
          y={H - 4}
          textAnchor="middle"
          className="drone-axis-label"
        >
          Per Hour of Footage
        </text>
      </svg>
    </g>
  );
}

// The same figure standing alone in HTML, at `width` px.
export function DroneMarginFigure({ width }: { width: number }) {
  return (
    <figure className="chart drone-margin-figure">
      <svg viewBox={`0 0 ${W} ${H}`} width={width} height={width * DRONE_MARGIN_ASPECT}>
        <DroneMargin x={0} y={0} w={W} />
      </svg>
    </figure>
  );
}

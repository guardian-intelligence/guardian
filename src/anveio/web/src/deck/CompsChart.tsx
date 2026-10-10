import {
  BILLION,
  DEMAND,
  GATES,
  MONTH_ZERO,
  HORIZON,
  UPSIDE_BILLION,
  monthIndex,
  planArr,
  upsideArr,
} from "~/model/phases";
import { money } from "./format";

// Gross run rates by months since each company's first ~$1M month (or
// launch). Sourced points only; mostly company claims. Every comp is an
// expert-data vendor billing $85+/hr, so none is like-for-like.
type Point = readonly [month: number, value: number];
type Comp = {
  name: string;
  points: readonly Point[];
  dotted?: boolean;
  // Hollow points that are inferred rather than reported.
  inferred?: readonly number[];
  label: { at: Point; dx: number; dy: number; anchor: "start" | "end" | "middle" };
};

const COMPS: readonly Comp[] = [
  {
    // m0 = Apr 2024, inferred from the founder's 17-month framing.
    name: "Mercor",
    points: [
      [0, 1e6],
      [10, 75e6],
      [11, 100e6],
      [17, 450e6],
      [26, 2e9],
    ],
    inferred: [0],
    label: { at: [26, 2e9], dx: 8, dy: 0, anchor: "start" },
  },
  {
    // m0 = Jan 2025 launch; no m0 point.
    name: "Handshake AI",
    points: [
      [8, 100e6],
      [12, 550e6],
      [15, 0.95e9],
    ],
    label: { at: [15, 0.95e9], dx: -8, dy: -8, anchor: "end" },
  },
  {
    // Month index from Jan 2025, not launch-aligned (footnoted).
    name: "micro1*",
    points: [
      [0, 7e6],
      [11, 100e6],
      [19, 500e6],
    ],
    dotted: true,
    label: { at: [19, 500e6], dx: 8, dy: 2, anchor: "start" },
  },
];

// Single reported points, no line; labels sit left of the dot.
const DOTS: readonly { name: string; at: Point; dy: number }[] = [
  { name: "Snorkel*", at: [12, 375e6], dy: -3 },
  { name: "Proximal*", at: [10, 200e6], dy: 1 },
];

// Real-world data comps: month 0 undisclosed, so a hatched range of months.
// Wirestock's label sits above its bar; Mecka's sits below, clear of the
// Proximal and Snorkel labels above it.
const RANGES = [
  { name: "Mecka >$100M", from: 2, to: 6, value: 100e6, below: true },
  { name: "Wirestock $40M", from: 29, to: 40, value: 40e6, below: false },
];

const W = 700;
const H = 244;
const m = { left: 44, right: 96, top: 12, bottom: H - 40 };
// Out to Sep 2031, so the upside's $1B month is drawn, not implied.
const X1 = 54;
const LOG0 = Math.log10(1e6);
const LOG1 = Math.log10(3e9);
const x = (month: number) => m.left + ((W - m.left - m.right) * month) / X1;
const y = (v: number) => m.bottom - ((m.bottom - m.top) * (Math.log10(v) - LOG0)) / (LOG1 - LOG0);
const line = (pts: readonly Point[]) =>
  pts.map(([mo, v], k) => `${k ? "L" : "M"}${x(mo).toFixed(1)},${y(v).toFixed(1)}`).join(" ");

const MONTHS = Array.from({ length: X1 + 1 }, (_, k) => k);
const at = (k: number) => MONTH_ZERO + k / 12;
const PLAN: Point[] = MONTHS.map((k) => [k, Math.max(1e6, planArr(at(k)))]);
const UPSIDE: Point[] = MONTHS.map((k) => [k, Math.max(1e6, upsideArr(at(k)))]);
const EXIT: Point = [monthIndex(GATES.p1.year), GATES.p1.plan];
const CROSS: Point | null = UPSIDE_BILLION ? [UPSIDE_BILLION.month, BILLION] : null;

const Y_TICKS = [1e6, 10e6, 100e6, 1e9];
const X_TICKS = [0, 6, 12, 18, 24, 30, 36, 42, 48, 54];

// The Jun 2029 plan as a share of a year’s real-world data spend.
const share = (spend: number) => `${Math.round((100 * GATES.p1.plan) / spend)}%`;
const BASE = DEMAND.find((d) => d.name === "Base")!;
const LOW = DEMAND.find((d) => d.name === "Low")!;

export function CompsChart() {
  return (
    <figure className="chart comps">
      <figcaption className="comps-title">
        How fast expert-data vendors scaled (not like-for-like)
        <span>
          Plan ≤ Mercor at every month ·{" "}
          {UPSIDE_BILLION
            ? `upside hits $1B at month ${UPSIDE_BILLION.month} (${UPSIDE_BILLION.label})`
            : `upside ${money(HORIZON.upside)} by Dec 2031`}
        </span>
      </figcaption>
      <p className="comps-legend">
        <span>
          <i className="key-line" /> Reported run-rate path
        </span>
        <span>
          <i className="key-dot" /> Single disclosure
        </span>
        <span>
          <i className="key-range" /> Real-world data claim range
        </span>
      </p>
      <svg
        viewBox={`0 0 ${W} ${H}`}
        role="img"
        aria-label={`Gross run rate by months since the first $1M month, log scale. Anveio plan reaches ${money(
          GATES.p1.plan,
        )} at month ${EXIT[0]}; Mercor reached $2B at month 26.`}
      >
        <defs>
          <pattern
            id="range-hatch"
            width="5"
            height="5"
            patternUnits="userSpaceOnUse"
            patternTransform="rotate(45)"
          >
            <line x1="0" y1="0" x2="0" y2="5" className="comp-hatch" />
          </pattern>
        </defs>
        {Y_TICKS.map((v) => (
          <g key={v}>
            <line x1={m.left} x2={x(X1)} y1={y(v)} y2={y(v)} className="grid" />
            <text
              x={m.left - 8}
              y={y(v)}
              className="tick"
              textAnchor="end"
              dominantBaseline="middle"
            >
              {money(v)}
            </text>
          </g>
        ))}
        <line x1={m.left} x2={x(X1)} y1={y(BILLION)} y2={y(BILLION)} className="target-rule" />
        <line x1={m.left} x2={x(X1)} y1={m.bottom} y2={m.bottom} className="axis" />
        {X_TICKS.map((k) => (
          <text key={k} x={x(k)} y={m.bottom + 16} className="tick" textAnchor="middle">
            {k}
          </text>
        ))}
        <text x={(m.left + x(X1)) / 2} y={m.bottom + 34} className="axis-note" textAnchor="middle">
          Months since first ~$1M month (Anveio: Mar 2027) · *not launch-aligned · Proximal = QTD ×
          4
        </text>
        {RANGES.map((r) => (
          <g key={r.name}>
            <rect
              x={x(r.from)}
              y={y(r.value) - 5}
              width={x(r.to) - x(r.from)}
              height={10}
              className="comp-range"
              fill="url(#range-hatch)"
            />
          </g>
        ))}
        {COMPS.map((c) => (
          <g key={c.name}>
            <path d={line(c.points)} className={c.dotted ? "comp-line comp-dotted" : "comp-line"} />
            {c.points.map(([mo, v], k) => (
              <circle
                key={mo}
                cx={x(mo)}
                cy={y(v)}
                r={3}
                className={c.inferred?.includes(k) ? "comp-dot-hollow" : "comp-dot"}
              />
            ))}
            <text
              x={x(c.label.at[0]) + c.label.dx}
              y={y(c.label.at[1]) + c.label.dy}
              textAnchor={c.label.anchor}
              className="comp-label"
            >
              {c.name}
            </text>
          </g>
        ))}
        {DOTS.map((d) => (
          <g key={d.name}>
            <circle cx={x(d.at[0])} cy={y(d.at[1])} r={4} className="comp-dot-hollow" />
            <text
              x={x(d.at[0]) - 9}
              y={y(d.at[1]) + d.dy}
              textAnchor="end"
              dominantBaseline="middle"
              className="comp-label"
            >
              {d.name}
            </text>
          </g>
        ))}
        {/* Range labels last, so their halo clears the comp lines. */}
        {RANGES.map((r) => (
          <text
            key={r.name}
            x={x(r.from)}
            y={r.below ? y(r.value) + 20 : y(r.value) - 10}
            className="comp-label"
          >
            {r.name}
          </text>
        ))}
        <path d={line(UPSIDE)} className="anveio-line anveio-upside" />
        <path d={line(PLAN)} className="anveio-line" />
        <circle cx={x(EXIT[0])} cy={y(EXIT[1])} r={4.5} className="anveio-dot" />
        <text x={x(EXIT[0]) + 8} y={y(EXIT[1]) + 16} className="anveio-note">
          {`Phase 1 exit ${money(EXIT[1])}`}
        </text>
        {CROSS && CROSS[0] <= X1 && (
          <g>
            <circle cx={x(CROSS[0])} cy={y(CROSS[1])} r={4.5} className="anveio-dot" />
            <text
              x={x(CROSS[0])}
              y={y(CROSS[1]) - 10}
              className="anveio-note"
              textAnchor="middle"
            >{`$1B · month ${CROSS[0]}`}</text>
          </g>
        )}
        <text x={x(X1) + 8} y={y(PLAN[X1]![1])} className="anveio-label" dominantBaseline="middle">
          Anveio plan
        </text>
        <text
          x={x(X1) + 8}
          y={y(UPSIDE[UPSIDE.length - 1]![1])}
          className="anveio-label"
          dominantBaseline="middle"
        >
          Upside
        </text>
      </svg>
      <p className="demand-bridge">
        <span>Demand</span>
        {`Jun 2029 plan = ${share(BASE.value)} of base-case spend (${money(
          BASE.value,
        )}/yr); in the low case (${money(LOW.value)}/yr) signed hours cap it.`}
      </p>
    </figure>
  );
}

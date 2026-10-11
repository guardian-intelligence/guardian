import { HORIZON } from "~/model/phases";
import { money } from "./format";

// Grameenphone: Tk 158.06bn 2025 revenue (Financial Express) at Tk121/$
// (US State Dept ICS 2025) ~ $1.3bn, audited. Anveio bars are Dec 2031 gross
// run rates (that month x 12) from the Roadmap model.
const GP = 1.3e9;
type Row = {
  name: string;
  basis: string;
  value: number;
  kind: "audited" | "plan" | "upside";
  // Small print under the basis.
  note?: string;
};
const ROWS: readonly Row[] = [
  { name: "Grameenphone", basis: "Audited 2025 revenue", value: GP, kind: "audited" },
  {
    name: "Anveio plan",
    basis: "Dec 2031 gross run rate",
    note: `net ${money(HORIZON.net)} after payouts`,
    value: HORIZON.plan,
    kind: "plan",
  },
  {
    name: "Anveio upside",
    basis: "Dec 2031 gross run rate",
    value: HORIZON.upside,
    kind: "upside",
  },
];

const W = 700;
const ROW = 70;
const m = { left: 178, right: 70, top: 30, bottom: 30 };
const H = m.top + ROW * ROWS.length + m.bottom;
const X1 = 1.5e9;
const x = (v: number) => m.left + ((W - m.left - m.right) * v) / X1;
const BAR = 30;
const pct = (v: number) => `${Math.round((100 * v) / GP)}%`;

export function RevenueClass() {
  return (
    <figure className="chart revenue-class">
      <figcaption className="comps-title">
        How close Phase 2 gets to Grameenphone’s revenue class
        <span>
          {`Dec 2031: plan at ${pct(HORIZON.plan)}, upside at ${pct(HORIZON.upside)} of Grameenphone.`}
        </span>
      </figcaption>
      <svg
        viewBox={`0 0 ${W} ${H}`}
        role="img"
        aria-label={ROWS.map((r) => `${r.name}, ${r.basis}: ${money(r.value)}`).join("; ")}
      >
        <defs>
          <pattern
            id="upside-hatch"
            width="6"
            height="6"
            patternUnits="userSpaceOnUse"
            patternTransform="rotate(45)"
          >
            <rect width="6" height="6" className="bar-hatch-bg" />
            <line x1="0" y1="0" x2="0" y2="6" className="bar-hatch-light" />
          </pattern>
        </defs>
        {[0, 0.5e9, 1e9, 1.5e9].map((v) => (
          <g key={v}>
            <line x1={x(v)} x2={x(v)} y1={m.top - 8} y2={H - m.bottom} className="grid" />
            <text x={x(v)} y={H - m.bottom + 18} className="tick" textAnchor="middle">
              {v ? money(v) : "$0"}
            </text>
          </g>
        ))}
        <line x1={x(GP)} x2={x(GP)} y1={m.top - 14} y2={H - m.bottom} className="target-rule" />
        <text x={x(GP)} y={m.top - 18} className="billion-label" textAnchor="middle">
          Grameenphone’s class
        </text>
        {ROWS.map((r, i) => {
          const cy = m.top + ROW * i + ROW / 2;
          return (
            <g key={r.name}>
              <text x={m.left - 14} y={cy - 4} className="bar-label" textAnchor="end">
                {r.name}
              </text>
              <text x={m.left - 14} y={cy + 14} className="margin-note" textAnchor="end">
                {r.basis}
              </text>
              {r.note && (
                <text x={m.left - 14} y={cy + 29} className="margin-note" textAnchor="end">
                  {r.note}
                </text>
              )}
              <rect
                x={m.left}
                y={cy - BAR / 2}
                width={x(r.value) - m.left}
                height={BAR}
                rx={3}
                className={
                  r.kind === "audited" ? "bar-cost" : r.kind === "plan" ? "bar" : undefined
                }
                fill={r.kind === "upside" ? "url(#upside-hatch)" : undefined}
              />
              <text x={x(r.value) + 10} y={cy} className="bar-value-sm" dominantBaseline="middle">
                {money(r.value)}
              </text>
            </g>
          );
        })}
      </svg>
    </figure>
  );
}

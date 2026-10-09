import { money } from "./format";

// A compact growth chart: one smooth curve per company from its founding (at
// zero) through every known figure, inside a shared time window so stacked
// charts line up. Each line ends beside its company's logo, tagged with its
// latest figure (and growth multiple); a one-line legend underneath names them. Used for revenue and funding.

export type RunRatePoint = { date: string; value: number };
export type RunRateSeries = {
  name: string;
  logo: string;
  // Approximate where the exact month isn't public.
  founded: string;
  points: readonly RunRatePoint[];
  // Hand-placed offset (chart units) from the line's end to its logo's centre,
  // chosen per series for clear space; far placements get a leader line.
  logoAt: { dx: number; dy: number };
};

// Plot area and the margins around it; the right margin leaves room for the
// logo-and-figure tags of lines ending at the window's edge.
const PLOT = { w: 400, h: 250 };
const M = { left: 76, right: 96, top: 14, bottom: 24 };
const LOGO = 16;
const W = M.left + PLOT.w + M.right;
const H = M.top + PLOT.h + M.bottom;

const toYear = (date: string) => {
  const [y, m, d] = date.split("-").map(Number);
  return y! + ((m ?? 1) - 1) / 12 + ((d ?? 1) - 1) / 365;
};

// "2026-10-08" -> "Q4 '26".
const quarterLabel = (date: string) => {
  const [y, m] = date.split("-").map(Number);
  return `Q${Math.floor(((m ?? 1) - 1) / 3) + 1} '${String(y).slice(-2)}`;
};

// Monotone cubic (Fritsch–Carlson) through (t, v): smooth, passes through
// every point (including the zero at founding), never overshoots between them.
function curve(points: readonly RunRatePoint[]) {
  const ts = points.map((p) => toYear(p.date));
  const ls = points.map((p) => p.value);
  const n = ts.length;
  const d = ts.slice(1).map((t, i) => (ls[i + 1]! - ls[i]!) / (t - ts[i]!));
  const m = ts.map((_, i) => (i === 0 ? d[0]! : i === n - 1 ? d[n - 2]! : (d[i - 1]! + d[i]!) / 2));
  for (let i = 0; i < n - 1; i++) {
    if (d[i] === 0) {
      m[i] = 0;
      m[i + 1] = 0;
      continue;
    }
    const a = m[i]! / d[i]!;
    const b = m[i + 1]! / d[i]!;
    const h = a * a + b * b;
    if (h > 9) {
      const k = 3 / Math.sqrt(h);
      m[i] = k * a * d[i]!;
      m[i + 1] = k * b * d[i]!;
    }
  }
  // Bend the final segment into an upward parabola: hold the slope leaving the
  // second-to-last point under the segment's average (flat when that point is
  // the founding zero), then set the arriving slope so the cubic reduces to a
  // quadratic (m0 + m1 = 2d), which accelerates into the latest figure.
  const k = n - 2;
  m[k] = k === 0 ? 0 : Math.min(m[k]!, 0.6 * d[k]!);
  m[k + 1] = 2 * d[k]! - m[k]!;
  return (t: number) => {
    let i = 0;
    while (i < n - 2 && t > ts[i + 1]!) i++;
    const h = ts[i + 1]! - ts[i]!;
    const u = (t - ts[i]!) / h;
    const h00 = 2 * u ** 3 - 3 * u ** 2 + 1;
    const h10 = u ** 3 - 2 * u ** 2 + u;
    const h01 = -2 * u ** 3 + 3 * u ** 2;
    const h11 = u ** 3 - u ** 2;
    return h00 * ls[i]! + h10 * h * m[i]! + h01 * ls[i + 1]! + h11 * h * m[i + 1]!;
  };
}

// Every y axis reads in millions of dollars, so the stacked charts compare.
const millions = (v: number) => Math.round(v / 1e6).toLocaleString("en-US");

// Three round y ticks: zero, half, and a top just above the data.
function axis(max: number) {
  const mag = 10 ** Math.floor(Math.log10(max));
  const top = [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10].map((k) => k * mag).find((k) => k >= max)!;
  return { top, ticks: [0, top / 2, top] };
}

export function GrowthChart({
  heading,
  title,
  series,
  from,
  asOf,
  multiple = false,
}: {
  // Short chart title above the plot; `title` labels the y axis.
  heading: string;
  title: string;
  series: readonly RunRateSeries[];
  // Shared time window, so stacked charts line up; earlier history is clipped.
  from: string;
  asOf: string;
  // Show each company's latest-over-first multiple beside its figure.
  multiple?: boolean;
}) {
  const t0 = toYear(from);
  const t1 = toYear(asOf);
  const { top, ticks } = axis(Math.max(...series.flatMap((s) => s.points.map((p) => p.value))));
  const x = (t: number) => M.left + (PLOT.w * (t - t0)) / (t1 - t0);
  const y = (v: number) => M.top + PLOT.h - (PLOT.h * v) / top;
  const id = title.replace(/\W+/g, "-");

  const lines = series.map((s) => {
    const pts = [{ date: s.founded, value: 0 }, ...s.points];
    const f = curve(pts);
    const a = toYear(s.founded);
    const b = toYear(s.points.at(-1)!.date);
    const steps = 300;
    const samples = Array.from({ length: steps + 1 }, (_, i) => a + ((b - a) * i) / steps).map(
      (t) => ({ x: x(t), y: y(f(t)) }),
    );
    // Fade every line in from transparent where it starts (its founding, or
    // the window's left edge for older companies) until it has left the
    // baseline, and over at least FADE units, so no line starts as a hard edge.
    const FADE = 48;
    const fadeFrom = Math.max(M.left, x(a));
    const lift = samples.find((p) => p.x >= fadeFrom && p.y <= M.top + PLOT.h - 10);
    const fadeTo = Math.max(lift?.x ?? fadeFrom, fadeFrom + FADE);
    return { s, samples, fadeFrom, fadeTo, end: { x: x(b), y: y(s.points.at(-1)!.value) } };
  });

  return (
    <figure className="chart growth">
      <figcaption className="growth-heading">{heading}</figcaption>
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={title}>
        <defs>
          <clipPath id={`logo-${id}`} clipPathUnits="objectBoundingBox">
            <rect width="1" height="1" rx="0.22" ry="0.22" />
          </clipPath>
          {lines.map(({ s, fadeFrom, fadeTo }, i) => (
            <linearGradient
              key={s.name}
              id={`fade-${id}-${i}`}
              gradientUnits="userSpaceOnUse"
              x1={fadeFrom}
              x2={Math.max(fadeTo, fadeFrom + 1)}
              y1={0}
              y2={0}
            >
              <stop offset="0" stopColor="var(--ink)" stopOpacity="0" />
              <stop offset="1" stopColor="var(--ink)" stopOpacity="1" />
            </linearGradient>
          ))}
          <clipPath id={`plot-${id}`}>
            <rect x={M.left} y={0} width={PLOT.w + M.right} height={H} />
          </clipPath>
        </defs>
        {/* The title runs up the y axis, centred on the plot. */}
        <text
          transform={`translate(10 ${M.top + PLOT.h / 2}) rotate(-90)`}
          className="growth-title"
          textAnchor="middle"
          dominantBaseline="central"
        >
          {`${title} ($M)`}
        </text>
        {ticks.map((v) => (
          <g key={v}>
            <line
              x1={M.left}
              x2={M.left + PLOT.w}
              y1={y(v)}
              y2={y(v)}
              className={v === 0 ? "axis" : "grid"}
            />
            <text
              x={M.left - 8}
              y={y(v)}
              className="tick"
              textAnchor="end"
              dominantBaseline="middle"
            >
              {millions(v)}
            </text>
          </g>
        ))}
        <text x={x(t0)} y={H - 6} className="tick" textAnchor="start">
          {quarterLabel(from)}
        </text>
        <text x={x(t1)} y={H - 6} className="tick" textAnchor="end">
          {quarterLabel(asOf)}
        </text>
        <g clipPath={`url(#plot-${id})`}>
          {lines.map(({ s, samples }, i) => (
            <path
              key={s.name}
              stroke={`url(#fade-${id}-${i})`}
              d={samples
                .map((p, i) => `${i ? "L" : "M"}${p.x.toFixed(1)},${p.y.toFixed(1)}`)
                .join(" ")}
              className="runrate-line"
            />
          ))}
        </g>
        {/* Each logo sits at its hand-placed spot beside the line's end. */}
        {lines.map(({ s, end }) => {
          const c = { x: end.x + s.logoAt.dx, y: end.y + s.logoAt.dy };
          const last = s.points.at(-1)!.value;
          const m = last / s.points[0]!.value;
          const dist = Math.hypot(s.logoAt.dx, s.logoAt.dy);
          const ux = s.logoAt.dx / dist;
          const uy = s.logoAt.dy / dist;
          return (
            <g key={s.name}>
              <title>{s.name}</title>
              {dist > LOGO * 1.5 && (
                <line
                  x1={end.x + ux * 3}
                  y1={end.y + uy * 3}
                  x2={c.x - ux * (LOGO / 2 + 3)}
                  y2={c.y - uy * (LOGO / 2 + 3)}
                  className="growth-leader"
                />
              )}
              {/* A badge behind every logo so transparent marks read alike. */}
              <rect
                x={c.x - LOGO / 2 - 1.5}
                y={c.y - LOGO / 2 - 1.5}
                width={LOGO + 3}
                height={LOGO + 3}
                rx={4.5}
                className="growth-badge"
              />
              <image
                href={s.logo}
                x={c.x - LOGO / 2}
                y={c.y - LOGO / 2}
                width={LOGO}
                height={LOGO}
                clipPath={`url(#logo-${id})`}
              />
              {/* The latest figure, and the growth multiple, tight to the logo's right. */}
              <text
                x={c.x + LOGO / 2 + 4}
                y={c.y}
                dominantBaseline="central"
                className="growth-tag"
              >
                {money(last)}
                {multiple && s.points.length > 1 && (
                  <tspan className="growth-tag-multiple" dx={3}>
                    {`${Number(m.toFixed(m < 10 ? 1 : 0))}×`}
                  </tspan>
                )}
              </text>
            </g>
          );
        })}
      </svg>
      <ul className="growth-legend">
        {[...series]
          .sort((a, b) => b.points.at(-1)!.value - a.points.at(-1)!.value)
          .map((s) => (
            <li key={s.name}>
              <img src={s.logo} alt="" />
              <span className="runrate-name">{s.name}</span>
            </li>
          ))}
      </ul>
    </figure>
  );
}

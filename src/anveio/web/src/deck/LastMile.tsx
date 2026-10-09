import { ECOSYSTEM } from "~/model/ecosystem";
import { Wings } from "./Brand";
import { GEO } from "./geo";

// We float over Bangladesh in two places. The data-collection arm takes data
// from the sites in Dumuria and sends it out through the AI economy: a band
// that thickens from data brokers to applied AI as the data turns into
// innovation, and returns to the deployment arm, which lands it in every
// region. Band thickness is a rough, monotonic estimate of value, not a
// measurement.
const W = 900;
const H = 540;

const MAP_H = 500;
const MS = MAP_H / GEO.country.height;
const MAP = { x: 150, y: 20 };
const onMap = (p: { x: number; y: number }) => ({ x: MAP.x + p.x * MS, y: MAP.y + p.y * MS });

const INSET_SIZE = 168;
const IS = INSET_SIZE / GEO.inset.size;
const INSET = { x: 8, y: 320 };
const onInset = (p: { x: number; y: number }) => ({
  x: INSET.x + p.x * IS,
  y: INSET.y + p.y * IS,
});

// The cycle is a circle; both arms sit on it, over the map's eastern edge.
const C = { x: 632, y: 272 };
const R = 224;
const rad = (deg: number) => (deg * Math.PI) / 180;
const pt = (deg: number, r = R) => ({
  x: C.x + r * Math.cos(rad(deg)),
  y: C.y + r * Math.sin(rad(deg)),
});
const COLLECT_ANGLE = 212;
const DEPLOY_ANGLE = 148 + 360;

const TILE = 58;
const HALF = TILE / 2;
const COLLECT = pt(COLLECT_ANGLE);
const DEPLOY = pt(DEPLOY_ANGLE);

// The band, clockwise from the collection arm to the deployment arm.
// Its ends stand clear of both arms' tiles.
const BAND = { from: COLLECT_ANGLE + 13, to: DEPLOY_ANGLE - 18, thin: 12, thick: 74 };
const thickness = (deg: number) =>
  BAND.thin + ((BAND.thick - BAND.thin) * (deg - BAND.from)) / (BAND.to - BAND.from);
const mix = (deg: number) => Math.round((100 * (deg - BAND.from)) / (BAND.to - BAND.from));

const SLICES = 160;
const SLICE_PATHS = Array.from({ length: SLICES }, (_, i) => {
  const step = (BAND.to - BAND.from) / SLICES;
  const a = BAND.from + step * i;
  // Overlap neighbours slightly so antialiasing leaves no seams.
  const b = Math.min(BAND.to, a + step * 1.4);
  const ta = thickness(a) / 2;
  const tb = thickness(b) / 2;
  const p = [pt(a, R + ta), pt(b, R + tb), pt(b, R - tb), pt(a, R - ta)];
  return {
    d: `M${p.map((q) => `${q.x.toFixed(1)},${q.y.toFixed(1)}`).join(" L")} Z`,
    mix: mix(a + step / 2),
  };
});

// Arrowhead into the deployment arm.
const HEAD = (() => {
  const half = thickness(BAND.to) / 2 + 5;
  const tip = pt(DEPLOY_ANGLE - 11.5);
  const a = pt(BAND.to, R + half);
  const b = pt(BAND.to, R - half);
  return `M${a.x.toFixed(1)},${a.y.toFixed(1)} L${tip.x.toFixed(1)},${tip.y.toFixed(1)} L${b.x.toFixed(1)},${b.y.toFixed(1)} Z`;
})();

// A thin, continuous guide ring just inside the band, coloured on the band's
// gradient. Each half carries its word centred along it, with the line
// broken only around the word;
// the second half runs along the bottom, so its text path is reversed to
// read upright.
const GUIDE_R = R - BAND.thick / 2 - 16;
const arcPath = (from: number, to: number, r: number) => {
  const n = Math.max(2, Math.ceil(Math.abs(to - from) / 2));
  return Array.from({ length: n + 1 }, (_, i) => pt(from + ((to - from) * i) / n, r))
    .map((q, i) => `${i ? "L" : "M"}${q.x.toFixed(1)},${q.y.toFixed(1)}`)
    .join(" ");
};
// Estimated label widths at the guide's type size, plus clear space each side.
const GUIDE_PAD = 10;
const GUIDES = (() => {
  const mid = (BAND.from + BAND.to) / 2;
  const halves = [
    { id: "data", label: "Data", width: 36, from: BAND.from, to: mid },
    { id: "innovation", label: "Innovation", width: 92, from: mid, to: BAND.to },
  ] as const;
  const degOf = (len: number) => (len / GUIDE_R) * (180 / Math.PI);
  return halves.map((h, k) => {
    const centre = (h.from + h.to) / 2;
    const half = degOf(h.width / 2 + GUIDE_PAD);
    const runs = [
      [h.from, centre - half],
      [centre + half, h.to],
    ] as const;
    const slices = runs.flatMap(([a, b]) => {
      const n = Math.max(1, Math.ceil((b - a) / 1.5));
      return Array.from({ length: n }, (_, i) => {
        const s0 = a + ((b - a) * i) / n;
        const s1 = a + ((b - a) * Math.min(n, i + 1.3)) / n;
        return { d: arcPath(s0, s1, GUIDE_R), mix: mix((s0 + s1) / 2) };
      });
    });
    return {
      ...h,
      slices,
      mix: mix(centre),
      textPath: k === 0 ? arcPath(h.from, h.to, GUIDE_R) : arcPath(h.to, h.from, GUIDE_R),
    };
  });
})();

// Logos in a tight grid inside the band, one equal section per stage in the
// order data passes through them. Columns step along the band; each column
// holds as many rows as the band is thick there.
const CELL = 25;
const LOGO_TILE = 23;
const LOGO = 17;
const LOGOS = (() => {
  const total = BAND.to - BAND.from;
  const colDeg = (CELL / R) * (180 / Math.PI);
  // Padding inside each end of a section, in columns.
  const PAD = 0.75;
  const rowsAt = (deg: number) => Math.max(1, Math.floor((thickness(deg) - 3) / CELL));
  const stages = ECOSYSTEM.map((stage) => stage.members.flatMap((m) => ("logo" in m ? [m] : [])));
  // Column heights for a section starting at `start`.
  const columns = (logos: readonly unknown[], start: number) => {
    const cols: number[] = [];
    for (let n = 0, deg = start + colDeg * (PAD + 0.5); n < logos.length; deg += colDeg) {
      const rows = Math.min(rowsAt(deg), logos.length - n);
      cols.push(rows);
      n += rows;
    }
    return cols;
  };
  // Size each section by the columns it needs, then lay it out again from
  // its real start (the band is thicker further along, so it may need fewer).
  const layout = (widths: readonly number[]) => {
    const sum = widths.reduce((a, b) => a + b, 0);
    let start = BAND.from;
    return stages.map((logos, k) => {
      const span = (total * widths[k]!) / sum;
      const section = { logos, start, span, cols: columns(logos, start) };
      start += span;
      return section;
    });
  };
  const even = stages.map(() => 1);
  const first = layout(even);
  const sections = layout(first.map((sec) => sec.cols.length + 2 * PAD));
  return sections.flatMap(({ logos, start, span, cols }) => {
    const shift = (span - cols.length * colDeg) / 2;
    let n = 0;
    return cols.flatMap((rows, j) => {
      const deg = start + shift + colDeg * (j + 0.5);
      return Array.from({ length: rows }, (_, r) => {
        const at = pt(deg, R + (r - (rows - 1) / 2) * CELL);
        return { ...logos[n++]!, x: at.x, y: at.y };
      });
    });
  });
})();

const SITES = [
  { id: "site-1", label: "Site 1", at: onInset(GEO.inset.sites.farm) },
  { id: "site-2", label: "Site 2", at: onInset(GEO.inset.sites.home) },
] as const;

type Point = { x: number; y: number };
const unit = (a: Point, b: Point) => {
  const d = Math.hypot(b.x - a.x, b.y - a.y);
  return { x: (b.x - a.x) / d, y: (b.y - a.y) / d };
};
const blend = (u: Point, v: Point) => unit({ x: 0, y: 0 }, { x: u.x + v.x, y: u.y + v.y });
// Stop a segment short of its end so arrowheads clear their target.
const toward = (a: Point, b: Point, by: number) => {
  const d = Math.hypot(b.x - a.x, b.y - a.y);
  return { x: b.x - ((b.x - a.x) * by) / d, y: b.y - ((b.y - a.y) * by) / d };
};
// A cubic leaving `p` along `dp` and arriving at `q` along `dq`, so branches
// meet their trunk tangentially.
const flow = (p: Point, dp: Point, q: Point, dq: Point) => {
  const l = Math.hypot(q.x - p.x, q.y - p.y) * 0.4;
  const f = (v: number) => v.toFixed(1);
  return `M${f(p.x)},${f(p.y)} C${f(p.x + dp.x * l)},${f(p.y + dp.y * l)} ${f(q.x - dq.x * l)},${f(q.y - dq.y * l)} ${f(q.x)},${f(q.y)}`;
};
// `gap` edges are masked around the Dumuria box on the country map.
type FlowEdge = { d: string; head: boolean; gap?: boolean };

// Data: the sites' tributaries join a short way out, inside the Dumuria
// detail; the trunk then runs through Dumuria on the country map (clearing
// its box) and on to the collection arm.
const DUMURIA_BOX = {
  x: MAP.x + GEO.country.focus.x * MS,
  y: MAP.y + GEO.country.focus.y * MS,
  width: GEO.country.focus.width * MS,
  height: GEO.country.focus.height * MS,
};
const DUMURIA_AT = {
  x: DUMURIA_BOX.x + DUMURIA_BOX.width / 2,
  y: DUMURIA_BOX.y + DUMURIA_BOX.height / 2,
};
// Clear space kept around the Dumuria box where the trunk passes through.
const DATA_GAP = 6;
const DATA_JOIN = {
  x: Math.max(...SITES.map((s) => s.at.x)) + 44,
  y: SITES.reduce((sum, s) => sum + s.at.y, 0) / SITES.length - 8,
};
const DATA_EDGES: readonly FlowEdge[] = (() => {
  const end = { x: COLLECT.x - HALF - 5, y: COLLECT.y };
  const out = unit(DATA_JOIN, DUMURIA_AT);
  const through = blend(out, unit(DUMURIA_AT, end));
  return [
    ...SITES.map((site) => ({
      d: flow(site.at, unit(site.at, DATA_JOIN), DATA_JOIN, out),
      head: false,
    })),
    {
      d: flow(DATA_JOIN, out, DUMURIA_AT, through),
      head: false,
      gap: true,
    },
    {
      d: flow(DUMURIA_AT, through, end, { x: 1, y: 0 }),
      head: true,
      gap: true,
    },
  ];
})();

// Innovation: one trunk out of the deployment arm, branching toward each
// region it reaches. Chittagong sits under the arm and Sylhet under the
// collection arm, so neither gets a branch.
// A leaf stops short of its point by `stop` (default ARRIVE_GAP), or clears
// a box around it.
type FlowNode = {
  at: Point;
  children?: readonly FlowNode[];
  stop?: number;
  clear?: typeof DUMURIA_BOX;
};
const ARRIVE_GAP = 12;
// Distance from a box's centre to its edge, plus DATA_GAP, along the line
// arriving from `from`.
const clearance = (from: Point, box: typeof DUMURIA_BOX) => {
  const c = { x: box.x + box.width / 2, y: box.y + box.height / 2 };
  const d = unit(from, c);
  const edge = Math.min(
    d.x ? box.width / 2 / Math.abs(d.x) : Infinity,
    d.y ? box.height / 2 / Math.abs(d.y) : Infinity,
  );
  return edge + DATA_GAP;
};
const region = (k: keyof typeof GEO.country.sites): FlowNode => ({
  at: onMap(GEO.country.sites[k]),
});
const DEPLOY_TREE: FlowNode = {
  at: { x: 362, y: 362 },
  children: [
    {
      at: { x: 326, y: 298 },
      children: [
        region("dhaka"),
        {
          at: { x: 262, y: 222 },
          children: [
            region("rangpur"),
            // Rajshahi city sits on the border river; stop well inside.
            { ...region("rajshahi"), stop: 28 },
          ],
        },
      ],
    },
    { at: DUMURIA_AT, clear: DUMURIA_BOX },
  ],
};
const DEPLOY_EDGES: readonly FlowEdge[] = (() => {
  const grow = (from: Point, fromDir: Point, node: FlowNode): FlowEdge[] => {
    const kids = node.children;
    const end = kids
      ? node.at
      : toward(from, node.at, node.clear ? clearance(from, node.clear) : (node.stop ?? ARRIVE_GAP));
    const centroid = kids && {
      x: kids.reduce((s, c) => s + c.at.x, 0) / kids.length,
      y: kids.reduce((s, c) => s + c.at.y, 0) / kids.length,
    };
    const dir = centroid ? blend(unit(from, node.at), unit(node.at, centroid)) : unit(from, end);
    return [
      {
        d: flow(from, fromDir, end, dir),
        head: !kids,
      },
      ...(kids ?? []).flatMap((c) => grow(node.at, dir, c)),
    ];
  };
  return grow({ x: DEPLOY.x - HALF, y: DEPLOY.y }, { x: -1, y: 0 }, DEPLOY_TREE);
})();

const focus = {
  x: MAP.x + GEO.country.focus.x * MS,
  y: MAP.y + GEO.country.focus.y * MS,
  width: GEO.country.focus.width * MS,
  height: GEO.country.focus.height * MS,
};

function Arm({ at }: { at: Point }) {
  return (
    <g transform={`translate(${at.x - HALF} ${at.y - HALF})`} filter="url(#float-shadow)">
      <rect width={TILE} height={TILE} rx={TILE * 0.24} className="mark-tile" />
      <Wings x={HALF} y={HALF} size={TILE * 0.62} />
    </g>
  );
}

export function LastMile() {
  return (
    <figure className="last-mile">
      <svg
        viewBox={`0 0 ${W} ${H}`}
        role="img"
        aria-label={`Our data-collection arm takes data from sites in Dumuria and sends it through the AI economy (${ECOSYSTEM.map((s) => s.name.toLowerCase()).join(", ")}), where it grows into innovation and returns to our deployment arm, which deploys it to every region of Bangladesh.`}
      >
        <defs>
          <mask id="data-gap" maskUnits="userSpaceOnUse" x={0} y={0} width={W} height={H}>
            <rect width={W} height={H} fill="white" />
            <rect
              x={DUMURIA_BOX.x - DATA_GAP}
              y={DUMURIA_BOX.y - DATA_GAP}
              width={DUMURIA_BOX.width + 2 * DATA_GAP}
              height={DUMURIA_BOX.height + 2 * DATA_GAP}
              fill="black"
            />
          </mask>
          <clipPath id="inset-clip">
            <rect width={GEO.inset.size} height={GEO.inset.size} />
          </clipPath>
          <filter id="float-shadow" x="-40%" y="-40%" width="180%" height="200%">
            <feDropShadow dx="0" dy="9" stdDeviation="7" floodOpacity="0.32" />
          </filter>
          <filter id="band-shadow" x="-10%" y="-10%" width="120%" height="125%">
            <feDropShadow dx="0" dy="6" stdDeviation="6" floodOpacity="0.2" />
          </filter>
          {(["data", "deploy"] as const).map((k) => (
            <marker
              key={k}
              id={`${k}-head`}
              viewBox="0 0 10 10"
              refX="8"
              refY="5"
              markerWidth="6"
              markerHeight="6"
              orient="auto"
            >
              <path d="M0,0.5 L9,5 L0,9.5 Z" className={`${k}-head`} />
            </marker>
          ))}
        </defs>

        {/* Bangladesh */}
        <g transform={`translate(${MAP.x} ${MAP.y}) scale(${MS})`}>
          {GEO.country.divisions.map((d, i) => (
            <path key={i} d={d} className="geo-land" />
          ))}
          <path d={GEO.country.dumuria} className="geo-focus-fill" />
        </g>
        <rect {...focus} className="geo-focus" />
        <line
          x1={focus.x}
          y1={focus.y}
          x2={INSET.x + INSET_SIZE}
          y2={INSET.y}
          className="geo-leader"
        />
        <line
          x1={focus.x}
          y1={focus.y + focus.height}
          x2={INSET.x + INSET_SIZE}
          y2={INSET.y + INSET_SIZE}
          className="geo-leader"
        />

        {/* Dumuria detail, beside Dumuria */}
        <g transform={`translate(${INSET.x} ${INSET.y})`}>
          <rect width={INSET_SIZE} height={INSET_SIZE} className="geo-inset-ground" />
          <g clipPath="url(#inset-clip)" transform={`scale(${IS})`}>
            <path d={GEO.inset.neighbours} className="geo-land geo-land-detail" />
            <path d={GEO.inset.dumuria} className="geo-dumuria" />
            <path d={GEO.inset.riverAreas} className="geo-river-area" />
            <path d={GEO.inset.riverLines} className="geo-river" />
          </g>
          <rect width={INSET_SIZE} height={INSET_SIZE} className="geo-inset-frame" />
          <text x={8} y={INSET_SIZE - 8} className="geo-label geo-label-major">
            Dumuria
          </text>
        </g>
        {SITES.map((site) => (
          <g key={site.id}>
            <circle cx={site.at.x} cy={site.at.y} r={3} className="site-dot" />
            <text
              x={site.at.x - 7}
              y={site.at.y + 3}
              className="geo-label geo-label-major"
              textAnchor="end"
            >
              {site.label}
            </text>
          </g>
        ))}

        {/* Flows on the ground: data in from the sites, innovation out to every region */}
        {DATA_EDGES.map((e) => (
          <path
            key={e.d}
            d={e.d}
            className="flow-data-in"
            markerEnd={e.head ? "url(#data-head)" : undefined}
            mask={e.gap ? "url(#data-gap)" : undefined}
          />
        ))}
        {DEPLOY_EDGES.map((e) => (
          <path
            key={e.d}
            d={e.d}
            className="flow-deploy"
            markerEnd={e.head ? "url(#deploy-head)" : undefined}
          />
        ))}

        {/* The cycle, floating above the map */}
        <g filter="url(#band-shadow)">
          {SLICE_PATHS.map((s, i) => (
            <path
              key={i}
              d={s.d}
              style={{
                fill: `color-mix(in oklab, var(--innovation) ${s.mix}%, var(--data))`,
              }}
            />
          ))}
          <path d={HEAD} style={{ fill: "var(--innovation)" }} />
        </g>
        {LOGOS.map((logo) => (
          <g key={logo.name}>
            <rect
              x={logo.x - LOGO_TILE / 2}
              y={logo.y - LOGO_TILE / 2}
              width={LOGO_TILE}
              height={LOGO_TILE}
              rx={4}
              className="cycle-logo-tile"
            />
            <image
              href={logo.logo}
              x={logo.x - LOGO / 2}
              y={logo.y - LOGO / 2}
              width={LOGO}
              height={LOGO}
              preserveAspectRatio="xMidYMid meet"
            >
              <title>{logo.name}</title>
            </image>
          </g>
        ))}
        {/* Guide ring: Data along the first half, Innovation along the second */}
        {GUIDES.map((g) => (
          <g key={g.id}>
            {g.slices.map((sl, i) => (
              <path
                key={i}
                d={sl.d}
                className="guide-line"
                style={{
                  stroke: `color-mix(in oklab, var(--innovation) ${sl.mix}%, var(--data))`,
                }}
              />
            ))}
            <path id={`guide-${g.id}`} d={g.textPath} fill="none" />
            <text
              className="flow-label guide-label"
              dominantBaseline="central"
              style={{ fill: `color-mix(in oklab, var(--innovation) ${g.mix}%, var(--data))` }}
            >
              <textPath href={`#guide-${g.id}`} startOffset="50%" textAnchor="middle">
                {g.label}
              </textPath>
            </text>
          </g>
        ))}

        {/* Our two arms */}
        <Arm at={COLLECT} />
        <Arm at={DEPLOY} />
      </svg>
    </figure>
  );
}

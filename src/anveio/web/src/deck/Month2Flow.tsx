import { accents } from "@guardian/brand/tokens";
import { Wings } from "./Brand";
import { ICONS } from "./ValueChain";

// Month 2 at a glance: build the pipeline, enrich the footage with labelled
// regions, sell it to buyers.

// Label classes and their outline colours (labelling-tool brights, chosen to
// read on green and gold fields).
const CLASSES = {
  crop: { name: "Crop", color: "#ffd60a" },
  water: { name: "Water", color: "#4cc9f0" },
  human: { name: "Human", color: "#ff4d8d" },
  road: { name: "Road", color: "#ff8c1a" },
  drainage: { name: "Drainage", color: "#b388ff" },
  embankment: { name: "Embankment", color: "#ffffff" },
} as const;
type ClassId = keyof typeof CLASSES;

// Regions in each still's own pixels (360 x 270); the tag sits at `tag`.
type Region = { cls: ClassId; points: string; tag: readonly [number, number] };
const FRAMES: readonly { file: string; regions: readonly Region[] }[] = [
  {
    file: "drone-1.jpg",
    regions: [
      {
        cls: "crop",
        points: "0,34 70,34 82,46 140,50 165,74 192,98 192,170 202,215 208,270 0,270",
        tag: [14, 150],
      },
      {
        cls: "water",
        points: "82,36 262,30 292,170 290,238 262,246 232,236 214,214 206,140 170,66 140,46",
        tag: [232, 60],
      },
      { cls: "human", points: "190,88 244,88 248,226 200,226", tag: [150, 214] },
      {
        cls: "embankment",
        points: "262,0 274,0 322,196 344,270 328,270 306,200",
        tag: [196, 262],
      },
    ],
  },
  {
    file: "drone-2.jpg",
    regions: [
      { cls: "crop", points: "224,0 360,0 360,270 224,270", tag: [262, 120] },
      { cls: "road", points: "150,0 210,0 210,270 150,270", tag: [154, 236] },
      { cls: "human", points: "150,118 184,118 184,152 150,152", tag: [100, 108] },
      { cls: "drainage", points: "211,0 223,0 223,270 211,270", tag: [214, 34] },
    ],
  },
  {
    file: "drone-3.jpg",
    regions: [
      { cls: "crop", points: "40,0 360,0 360,206 260,150 150,82", tag: [230, 30] },
      { cls: "road", points: "0,18 330,226 360,236 360,270 0,270", tag: [24, 196] },
      { cls: "human", points: "236,140 276,140 276,176 236,176", tag: [270, 176] },
    ],
  },
];

const W = 1184;
// The row sits so the top still meets the canvas top.
const ROW = 63 + 18 + 4;
const R = 54;
const CLEAR = 16;
// The labelled stills overlap in a short cascade: each later still sits
// STEP right of and below the one before, front still first.
const FRAME = { w: 168, h: 126 };
const STEP = { x: 44, y: 18 };
const STILL = { w: 360, h: 270 };
// Stage labels sit under the cascade's lowest still.
const LABEL_Y = ROW + FRAME.h / 2 + STEP.y + 40;
// The canvas hugs the drawing: top still to the labels' descenders.
const H = LABEL_Y + 8;

// Left to right: Anveio, the cascade of stills, the Buyers station (as on
// What we do), centred, with equal arrows between them.
const FRAMES_W = FRAME.w + 2 * STEP.x;
const ARROW = 128;
const TOTAL = 2 * R + ARROW + FRAMES_W + ARROW + 2 * R;
const US_X = (W - TOTAL) / 2 + R;
const FRAMES_X = US_X + R + ARROW;
const BUYERS_X = FRAMES_X + FRAMES_W + ARROW + R;
const ICON = 34;

function Frame({
  x,
  y,
  file,
  regions,
}: {
  x: number;
  y: number;
  file: string;
  regions: readonly Region[];
}) {
  return (
    <svg x={x} y={y} width={FRAME.w} height={FRAME.h} viewBox={`0 0 ${STILL.w} ${STILL.h}`}>
      <g clipPath="url(#m2-thumb)">
        <image
          href={`/deck/${file}`}
          width={STILL.w}
          height={STILL.h}
          preserveAspectRatio="xMidYMid slice"
        />
        {regions.map((r) => (
          <polygon
            key={r.cls}
            points={r.points}
            className="label-region"
            style={{ stroke: CLASSES[r.cls].color, fill: CLASSES[r.cls].color }}
          />
        ))}
        {regions.map((r) => {
          const name = CLASSES[r.cls].name;
          const w = name.length * 13 + 14;
          return (
            <g key={r.cls}>
              <rect
                x={r.tag[0]}
                y={r.tag[1] - 22}
                width={w}
                height={26}
                rx={4}
                style={{ fill: CLASSES[r.cls].color }}
              />
              <text x={r.tag[0] + 7} y={r.tag[1] - 3} className="label-tag">
                {name}
              </text>
            </g>
          );
        })}
      </g>
      <rect
        width={STILL.w}
        height={STILL.h}
        rx={18}
        className="card-edge"
        style={{ strokeWidth: 2 }}
      />
    </svg>
  );
}

export function Month2Flow() {
  return (
    <figure className="chart month-2-flow">
      <svg
        viewBox={`0 0 ${W} ${H}`}
        role="img"
        aria-label="Anveio builds the data pipeline, enriches the drone footage with labelled regions such as crop, water, humans, roads and drainage, then sells it to buyers."
      >
        <defs>
          <marker
            id="m2-head"
            viewBox="0 0 10 10"
            refX="8"
            refY="5"
            markerWidth="5"
            markerHeight="5"
            orient="auto"
          >
            <path d="M1,1.5 L8,5 L1,8.5" className="chain-head-strong" />
          </marker>
          <clipPath id="m2-thumb" clipPathUnits="objectBoundingBox">
            <rect width="1" height="1" rx="0.05" ry="0.067" />
          </clipPath>
        </defs>

        <circle cx={US_X} cy={ROW} r={R} style={{ fill: accents.ink }} />
        <Wings x={US_X} y={ROW} size={R * 0.95} />
        <text x={US_X} y={LABEL_Y} className="flow-name" textAnchor="middle">
          Create data pipeline
        </text>

        <line
          x1={US_X + R + CLEAR}
          x2={FRAMES_X - CLEAR}
          y1={ROW}
          y2={ROW}
          className="chain-flow chain-flow-strong"
          markerEnd="url(#m2-head)"
        />
        {FRAMES.map((f, i) => ({ f, i }))
          .reverse()
          .map(({ f, i }) => (
            <Frame
              key={f.file}
              x={FRAMES_X + i * STEP.x}
              y={ROW - FRAME.h / 2 + (i - 1) * STEP.y}
              file={f.file}
              regions={f.regions}
            />
          ))}
        <text x={FRAMES_X + FRAMES_W / 2} y={LABEL_Y} className="flow-name" textAnchor="middle">
          Enrich dataset
        </text>
        <line
          x1={FRAMES_X + FRAMES_W + CLEAR}
          x2={BUYERS_X - R - CLEAR}
          y1={ROW}
          y2={ROW}
          className="chain-flow chain-flow-strong"
          markerEnd="url(#m2-head)"
        />
        <circle cx={BUYERS_X} cy={ROW} r={R} className="chain-node" />
        <svg
          x={BUYERS_X - ICON / 2}
          y={ROW - ICON / 2 - 10}
          width={ICON}
          height={ICON}
          viewBox="0 0 24 24"
          className="chain-icon"
        >
          {ICONS.buyers}
        </svg>
        <text x={BUYERS_X} y={ROW + 22} className="chain-name" textAnchor="middle">
          Buyers
        </text>
        <text x={BUYERS_X} y={LABEL_Y} className="flow-name" textAnchor="middle">
          Sell
        </text>
      </svg>
    </figure>
  );
}

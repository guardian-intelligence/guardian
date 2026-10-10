import { accents } from "@guardian/brand/tokens";
import { Wings } from "./Brand";
import { CARD, CardStack, SAMPLE_GROUPS, STACK_H, STACK_W } from "./ValueChain";

// Month 1 at a glance: deploy the drone, collect the footage, upload it to
// Anveio; then again the next day at about the same time.

// Clock (24px grid, Lucide geometry) for the daily loop.
const CLOCK = (
  <>
    <circle cx="12" cy="12" r="10" />
    <path d="M12 6v6l4 2" />
  </>
);

const W = 1184;
const H = 380;
const ROW = 200;
const R = 62;
const CLEAR = 16;
// The footage stack is drawn at twice the What We Do key; the drone photo
// matches the stack's footprint.
const SCALE = 2;
const STACK = { w: STACK_W * SCALE, h: STACK_H * SCALE };
const PHOTO = { w: STACK.w, h: STACK.h, r: CARD.r * SCALE };
const LABEL_Y = ROW + STACK.h / 2 + 34;
const GAP = 360;
const XS = [W / 2 - GAP, W / 2, W / 2 + GAP] as const;
const HALF = [PHOTO.w / 2, STACK.w / 2, R] as const;
// The daily loop arcs over the row, from Anveio back to the drone.
const LOOP_Y = ROW - STACK.h / 2 - 78;
const LOOP_R = 28;

export function Month1Flow() {
  const drone = SAMPLE_GROUPS.find((g) => g.label === "Drone")!;
  const [px, sx, ux] = XS;
  const loop = [
    `M${ux},${ROW - R - CLEAR}`,
    `L${ux},${LOOP_Y + LOOP_R}`,
    `Q${ux},${LOOP_Y} ${ux - LOOP_R},${LOOP_Y}`,
    `L${px + LOOP_R},${LOOP_Y}`,
    `Q${px},${LOOP_Y} ${px},${LOOP_Y + LOOP_R}`,
    `L${px},${ROW - PHOTO.h / 2 - CLEAR}`,
  ].join(" ");
  return (
    <figure className="chart month-1-flow">
      <svg
        viewBox={`0 0 ${W} ${H}`}
        role="img"
        aria-label="Deploy the drone to the farm, collect the footage, upload it to Anveio; repeat every day at about the same time."
      >
        <defs>
          <marker
            id="flow-head"
            viewBox="0 0 10 10"
            refX="8"
            refY="5"
            markerWidth="5"
            markerHeight="5"
            orient="auto"
          >
            <path d="M1,1.5 L8,5 L1,8.5" className="chain-head-strong" />
          </marker>
          <clipPath id="flow-thumb" clipPathUnits="objectBoundingBox">
            <rect width="1" height="1" rx="0.05" ry="0.07" />
          </clipPath>
        </defs>

        {([0, 1] as const).map((i) => (
          <line
            key={i}
            x1={XS[i] + HALF[i] + CLEAR}
            x2={XS[i + 1]! - HALF[i + 1]! - CLEAR}
            y1={ROW}
            y2={ROW}
            className="chain-flow chain-flow-strong"
            markerEnd="url(#flow-head)"
          />
        ))}
        <path d={loop} className="chain-flow chain-flow-strong" markerEnd="url(#flow-head)" />
        <g className="flow-loop-label">
          <rect x={sx - 70} y={LOOP_Y - 18} width={140} height={36} className="card-back" />
          <svg
            x={sx - 54}
            y={LOOP_Y - 11}
            width={22}
            height={22}
            viewBox="0 0 24 24"
            className="chain-icon"
          >
            {CLOCK}
          </svg>
          <text
            x={sx + 14}
            y={LOOP_Y}
            className="flow-name"
            textAnchor="middle"
            dominantBaseline="middle"
          >
            Repeat
          </text>
        </g>

        <image
          href="/deck/drone-mavic3m.jpg"
          x={px - PHOTO.w / 2}
          y={ROW - PHOTO.h / 2}
          width={PHOTO.w}
          height={PHOTO.h}
          preserveAspectRatio="xMidYMid slice"
          clipPath="url(#flow-thumb)"
        />
        <rect
          x={px - PHOTO.w / 2}
          y={ROW - PHOTO.h / 2}
          width={PHOTO.w}
          height={PHOTO.h}
          rx={PHOTO.r}
          className="card-edge"
        />
        <text x={px} y={LABEL_Y} className="flow-name" textAnchor="middle">
          Deploy drone to farm
        </text>

        <g transform={`translate(${sx - STACK.w / 2} ${ROW - STACK.h / 2}) scale(${SCALE})`}>
          <CardStack x={0} y={0} files={drone.files} label="" clip="flow-thumb" />
        </g>
        <text x={sx} y={LABEL_Y} className="flow-name" textAnchor="middle">
          Collect footage
        </text>

        <circle cx={ux} cy={ROW} r={R} style={{ fill: accents.ink }} />
        <Wings x={ux} y={ROW} size={R * 0.95} />
        <text x={ux} y={LABEL_Y} className="flow-name" textAnchor="middle">
          Upload footage
        </text>
      </svg>
    </figure>
  );
}

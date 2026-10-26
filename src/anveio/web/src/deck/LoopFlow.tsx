import type { ReactNode } from "react";
import { accents } from "@guardian/brand/tokens";
import { Wings } from "./Brand";

// A month at a glance: Anveio starts something, then a loop of steps runs
// round and round. The loop arcs over the row from the last step back to the
// first step, or all the way back to Anveio's own step.

// Icons on a 24px grid, Lucide geometry.
export const ICONS = {
  clock: (
    <>
      <circle cx="12" cy="12" r="10" />
      <path d="M12 6v6l4 2" />
    </>
  ),
  video: (
    <>
      <path d="m16 13 5.223 3.482a.5.5 0 0 0 .777-.416V7.87a.5.5 0 0 0-.752-.432L16 10.5" />
      <rect x="2" y="6" width="14" height="12" rx="2" />
    </>
  ),
  pipeline: (
    <>
      <rect width="8" height="8" x="3" y="3" rx="2" />
      <path d="M7 11v4a2 2 0 0 0 2 2h4" />
      <rect width="8" height="8" x="13" y="13" rx="2" />
    </>
  ),
  money: (
    <>
      <circle cx="12" cy="12" r="10" />
      <path d="M16 8h-6a2 2 0 1 0 0 4h4a2 2 0 1 1 0 4H8" />
      <path d="M12 18V6" />
    </>
  ),
  improve: (
    <>
      <path d="M16 7h6v6" />
      <path d="m22 7-8.5 8.5-5-5L2 17" />
    </>
  ),
} as const;

// A step Anveio does itself shows the Anveio mark instead of an icon.
export type LoopStep = { label: string; icon: ReactNode } | { label: string; anveio: true };

const W = 1184;
const H = 380;
const ROW = 220;
const R = 62;
const CLEAR = 16;
const GAP = 300;
const LABEL_Y = ROW + R + 34;
const LOOP_Y = ROW - R - 70;
const LOOP_R = 28;
const ICON = 52;

export function LoopFlow({
  id,
  start,
  steps,
  label,
  loopTo = "first-step",
  loop: loopName = { text: "Repeat", icon: ICONS.clock },
}: {
  // Prefixes the marker id, so two diagrams on one page don't share it.
  id: string;
  start: string;
  steps: readonly [LoopStep, LoopStep, LoopStep];
  label: string;
  loopTo?: "start" | "first-step";
  loop?: { text: string; icon: ReactNode };
}) {
  const xs = [0, 1, 2, 3].map((i) => W / 2 + (i - 1.5) * GAP);
  const first = xs[loopTo === "start" ? 0 : 1]!;
  const last = xs[3]!;
  const loop = [
    `M${last},${ROW - R - CLEAR}`,
    `L${last},${LOOP_Y + LOOP_R}`,
    `Q${last},${LOOP_Y} ${last - LOOP_R},${LOOP_Y}`,
    `L${first + LOOP_R},${LOOP_Y}`,
    `Q${first},${LOOP_Y} ${first},${LOOP_Y + LOOP_R}`,
    `L${first},${ROW - R - CLEAR}`,
  ].join(" ");
  const mid = (first + last) / 2;
  const head = `${id}-head`;
  return (
    <figure className="chart loop-flow">
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={label}>
        <defs>
          <marker
            id={head}
            viewBox="0 0 10 10"
            refX="8"
            refY="5"
            markerWidth="5"
            markerHeight="5"
            orient="auto"
          >
            <path d="M1,1.5 L8,5 L1,8.5" className="chain-head-strong" />
          </marker>
        </defs>

        {[0, 1, 2].map((i) => (
          <line
            key={i}
            x1={xs[i]! + R + CLEAR}
            x2={xs[i + 1]! - R - CLEAR}
            y1={ROW}
            y2={ROW}
            className="chain-flow chain-flow-strong"
            markerEnd={`url(#${head})`}
          />
        ))}
        <path d={loop} className="chain-flow chain-flow-strong" markerEnd={`url(#${head})`} />
        <g>
          <rect x={mid - 70} y={LOOP_Y - 18} width={140} height={36} className="card-back" />
          <svg
            x={mid - 54}
            y={LOOP_Y - 11}
            width={22}
            height={22}
            viewBox="0 0 24 24"
            className="chain-icon"
          >
            {loopName.icon}
          </svg>
          <text
            x={mid + 14}
            y={LOOP_Y}
            className="flow-name"
            textAnchor="middle"
            dominantBaseline="middle"
          >
            {loopName.text}
          </text>
        </g>

        {[{ label: start, anveio: true } as LoopStep, ...steps].map((s, i) => {
          const x = xs[i]!;
          return (
            <g key={s.label}>
              {"anveio" in s ? (
                <>
                  <circle cx={x} cy={ROW} r={R} style={{ fill: accents.ink }} />
                  <Wings x={x} y={ROW} size={R * 0.95} />
                </>
              ) : (
                <>
                  <circle cx={x} cy={ROW} r={R} className="loop-node" />
                  <svg
                    x={x - ICON / 2}
                    y={ROW - ICON / 2}
                    width={ICON}
                    height={ICON}
                    viewBox="0 0 24 24"
                    className="chain-icon"
                  >
                    {s.icon}
                  </svg>
                </>
              )}
              <text x={x} y={LABEL_Y} className="flow-name" textAnchor="middle">
                {s.label}
              </text>
            </g>
          );
        })}
      </svg>
    </figure>
  );
}

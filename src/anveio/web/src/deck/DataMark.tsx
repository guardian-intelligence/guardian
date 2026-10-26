// The data label: an italic serif D with one plus per stage of refinement as
// a subscript (none for raw footage). The pluses are drawn crosses: one alone,
// two on a diagonal (bottom-left to top-right), three as a triangle.

export type Grade = 0 | 1 | 2 | 3;

const D_WIDTH = 0.62;
// Plus geometry relative to the D's font size.
const ARM = 0.07;
const STROKE = 0.058;
const STEP = 0.24;

// Plus centres in steps: x to the right, y upward.
const PLUSES: Record<Grade, ReadonlyArray<readonly [number, number]>> = {
  0: [],
  1: [[0, 0]],
  2: [
    [0, 0],
    [1, 1],
  ],
  // Equilateral, so all three gaps match.
  3: [
    [0, 0],
    [1, 0],
    [0.5, Math.sqrt(3) / 2],
  ],
};

const subWidth = (size: number, grade: Grade) => {
  const cols = PLUSES[grade].map(([c]) => c);
  return cols.length ? (Math.max(...cols) * STEP + 2.8 * ARM) * size : 0;
};

// Centred on (x, y).
export function DataMark({
  x,
  y,
  size,
  grade,
  strong = false,
}: {
  x: number;
  y: number;
  size: number;
  grade: Grade;
  // Full black pluses, to match a black flow line.
  strong?: boolean;
}) {
  const dWidth = size * D_WIDTH;
  const left = x - (dWidth + subWidth(size, grade)) / 2;
  const subLeft = left + dWidth + ARM * size * 0.8;
  const baseY = y + size * 0.3;
  const arm = ARM * size;
  return (
    <g>
      <text
        x={left + dWidth / 2}
        y={y}
        className="data-mark"
        style={{ fontSize: size }}
        textAnchor="middle"
        dominantBaseline="central"
      >
        D
      </text>
      {PLUSES[grade].length > 0 && (
        <path
          d={PLUSES[grade]
            .map(([c, r]) => {
              const cx = subLeft + arm + c * STEP * size;
              const cy = baseY - r * STEP * size;
              return `M${cx - arm},${cy} H${cx + arm} M${cx},${cy - arm} V${cy + arm}`;
            })
            .join(" ")}
          className={strong ? "data-mark-sub data-mark-sub-strong" : "data-mark-sub"}
          strokeWidth={STROKE * size}
        />
      )}
    </g>
  );
}

// Half the drawn width of a DataMark, for the gap it cuts in a flow line.
export const dataMarkHalf = (size: number, grade: Grade) =>
  (size * D_WIDTH + subWidth(size, grade)) / 2;

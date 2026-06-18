import {
  TAM_SERIES,
  formatQuarterShort,
  formatTamBillion,
  type TamProjectionPoint,
} from "./model";

// SSR-safe summary table. This ALWAYS renders — it is the non-JS reading path
// and the screen-reader-friendly data surface for the projection. The canvas
// chart is a progressive enhancement layered on top of this; if hydration
// never happens, the reader still gets every number.
//
// We show milestone quarters (the start point plus each year's Q4) rather than
// all eighteen rows: enough to read the shape of the ramp without turning the
// article into a spreadsheet.

function milestonePoints(
  points: readonly TamProjectionPoint[],
): readonly TamProjectionPoint[] {
  return points.filter((point, index) => index === 0 || point.quarter.endsWith("Q4"));
}

const META_LABEL =
  "font-mono text-[10px] font-semibold uppercase";

export function TamSummaryTable({ points }: { points: readonly TamProjectionPoint[] }) {
  const rows = milestonePoints(points);

  return (
    <div className="w-full overflow-x-auto" data-tam-summary-table>
      <table
        className="w-full border-collapse text-left"
        style={{
          fontFamily: "'Geist', sans-serif",
          fontSize: "13px",
          color: "var(--treatment-muted-strong)",
          fontVariantNumeric: "tabular-nums",
        }}
      >
        <caption
          className={`${META_LABEL} pb-3 text-left`}
          style={{
            color: "var(--treatment-muted-meta)",
            letterSpacing: "0.2em",
          }}
        >
          Projected TAM by quarter · scenario
        </caption>
        <thead>
          <tr style={{ borderBottom: "1px solid var(--treatment-hairline)" }}>
            <Th>Quarter</Th>
            <Th align="right">Cloud TAM</Th>
            {TAM_SERIES.map((series) => (
              <Th key={series.key} align="right">
                {series.short}
              </Th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((point) => (
            <tr
              key={point.quarter}
              style={{ borderBottom: "1px solid var(--treatment-hairline)" }}
            >
              <Td>{formatQuarterShort(point.quarter)}</Td>
              <Td align="right" muted>
                {formatTamBillion(point.cloudTamBillion)}
              </Td>
              {TAM_SERIES.map((series) => (
                <Td
                  key={series.key}
                  align="right"
                  strong={series.emphasis === "thesis"}
                >
                  {formatTamBillion(point[series.key])}
                </Td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Th({
  children,
  align = "left",
}: {
  children: React.ReactNode;
  align?: "left" | "right";
}) {
  return (
    <th
      scope="col"
      className="font-mono text-[10px] font-semibold uppercase"
      style={{
        padding: "0 0 8px",
        textAlign: align,
        color: "var(--treatment-muted-meta)",
        letterSpacing: "0.12em",
        fontVariationSettings: '"wght" 600',
        whiteSpace: "nowrap",
      }}
    >
      {children}
    </th>
  );
}

function Td({
  children,
  align = "left",
  muted,
  strong,
}: {
  children: React.ReactNode;
  align?: "left" | "right";
  muted?: boolean;
  strong?: boolean;
}) {
  return (
    <td
      style={{
        padding: "9px 0",
        paddingLeft: align === "right" ? "16px" : 0,
        textAlign: align,
        whiteSpace: "nowrap",
        color: strong
          ? "var(--treatment-ink)"
          : muted
            ? "var(--treatment-muted)"
            : "var(--treatment-muted-strong)",
        fontWeight: strong ? 600 : 400,
      }}
    >
      {children}
    </td>
  );
}

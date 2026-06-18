"use client";

import { scaleLinear } from "d3-scale";
import { line as d3line, type Line } from "d3-shape";
import { layoutWithLines, prepareWithSegments } from "@chenglou/pretext";
import { useEffect, useRef, useState } from "react";
import {
  TAM_SERIES,
  formatQuarterShort,
  formatTamBillion,
  type TamProjectionPoint,
} from "./model";

// Client-only canvas chart. The summary table is the SSR/no-JS reading path;
// this is the progressive enhancement on top of it, so it renders nothing until
// mounted. Canvas (not SVG) is deliberate: no static SVG asset, nothing to
// export, everything recomputes from the URL-state values on the client.
//
// Thin layer, by construction:
//   - d3-scale builds the linear x/y scales,
//   - d3-shape strokes each series path straight onto the 2D context,
//   - @chenglou/pretext lays out the (wrappable) legend labels without DOM
//     reflow, so a narrow mobile chart wraps "Default for new software
//     companies" cleanly instead of clipping it.
//
// There are no animations or transitions, so prefers-reduced-motion needs no
// special handling — a lever change repaints synchronously, full stop.

const PAD = { top: 18, right: 76, bottom: 30, left: 48 } as const;
const LEGEND_FONT = "12px 'Geist', system-ui, sans-serif";
const AXIS_FONT = "11px 'Geist', system-ui, sans-serif";

interface SeriesColors {
  readonly cloudIndexedBareMetalTamBillion: string;
  readonly pcBuilderTamBillion: string;
  readonly defaultSoftwareCompanyTamBillion: string;
  readonly flare: string;
  readonly grid: string;
  readonly axis: string;
}

const FALLBACK_COLORS: SeriesColors = {
  cloudIndexedBareMetalTamBillion: "rgba(11,11,11,0.42)",
  pcBuilderTamBillion: "rgba(11,11,11,0.66)",
  defaultSoftwareCompanyTamBillion: "#0b0b0b",
  flare: "#ccff00",
  grid: "rgba(11,11,11,0.10)",
  axis: "rgba(11,11,11,0.55)",
};

function lineWeight(emphasis: "base" | "mid" | "thesis"): number {
  return emphasis === "thesis" ? 2.5 : 1.5;
}

function resolveColors(host: HTMLElement): SeriesColors {
  const style = getComputedStyle(host);
  const read = (name: string, fallback: string) => {
    const value = style.getPropertyValue(name).trim();
    return value || fallback;
  };
  return {
    cloudIndexedBareMetalTamBillion: read(
      "--treatment-muted",
      FALLBACK_COLORS.cloudIndexedBareMetalTamBillion,
    ),
    pcBuilderTamBillion: read(
      "--treatment-muted-strong",
      FALLBACK_COLORS.pcBuilderTamBillion,
    ),
    defaultSoftwareCompanyTamBillion: read(
      "--treatment-ink",
      FALLBACK_COLORS.defaultSoftwareCompanyTamBillion,
    ),
    flare: read("--color-flare", FALLBACK_COLORS.flare),
    grid: read("--treatment-hairline", FALLBACK_COLORS.grid),
    axis: read("--treatment-muted-meta", FALLBACK_COLORS.axis),
  };
}

export function TamChart({ points }: { points: readonly TamProjectionPoint[] }) {
  const hostRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [mounted, setMounted] = useState(false);
  const [size, setSize] = useState({ width: 0, height: 0 });
  const [hover, setHover] = useState<number | null>(null);

  useEffect(() => setMounted(true), []);

  // Track the host's pixel box. The container owns its height via aspect-ratio
  // so dimensions stay stable across lever changes (no reflow jump).
  useEffect(() => {
    if (!mounted) return;
    const host = hostRef.current;
    if (!host) return;
    const observer = new ResizeObserver(() => {
      const rect = host.getBoundingClientRect();
      setSize({ width: rect.width, height: rect.height });
    });
    observer.observe(host);
    return () => observer.disconnect();
  }, [mounted]);

  // Repaint whenever the data, the size, or the hover index changes.
  useEffect(() => {
    const canvas = canvasRef.current;
    const host = hostRef.current;
    if (!canvas || !host || size.width === 0 || size.height === 0) return;
    drawChart(canvas, size.width, size.height, points, resolveColors(host), hover);
  }, [points, size, hover]);

  if (!mounted) {
    // No-JS / pre-hydration: the summary table carries the data. Reserve the
    // aspect-ratio box so hydration doesn't shift the article.
    return <div aria-hidden style={{ width: "100%", aspectRatio: "16 / 9" }} />;
  }

  const hoverPoint = hover === null ? null : points[hover];

  return (
    <figure className="relative m-0 w-full" data-tam-chart>
      <div
        ref={hostRef}
        className="relative w-full"
        style={{ aspectRatio: "16 / 9", minHeight: "260px" }}
      >
        <canvas
          ref={canvasRef}
          role="img"
          aria-label="Projected direct-to-consumer bare metal TAM by quarter, three scenario lines from 2026Q3 to 2030Q4."
          className="absolute inset-0 h-full w-full"
          style={{ touchAction: "none" }}
          onPointerMove={(event) => {
            const rect = event.currentTarget.getBoundingClientRect();
            setHover(nearestIndex(event.clientX - rect.left, rect.width, points.length));
          }}
          onPointerLeave={() => setHover(null)}
        />
        {hoverPoint ? <HoverReadout point={hoverPoint} /> : null}
      </div>
    </figure>
  );
}

function nearestIndex(relX: number, width: number, count: number): number | null {
  const plotW = width - PAD.left - PAD.right;
  if (plotW <= 0 || count < 2) return null;
  const fraction = (relX - PAD.left) / plotW;
  const index = Math.round(fraction * (count - 1));
  return Math.min(count - 1, Math.max(0, index));
}

function HoverReadout({ point }: { point: TamProjectionPoint }) {
  return (
    <div
      className="pointer-events-none absolute right-2 top-2 flex flex-col gap-1 rounded-sm px-3 py-2"
      style={{
        background: "var(--treatment-ground)",
        border: "1px solid var(--treatment-hairline)",
        boxShadow: "0 1px 2px rgba(11,11,11,0.08)",
      }}
    >
      <span
        className="font-mono text-[10px] font-semibold uppercase"
        style={{ color: "var(--treatment-muted-meta)", letterSpacing: "0.12em" }}
      >
        {formatQuarterShort(point.quarter)}
      </span>
      {TAM_SERIES.map((series) => (
        <span
          key={series.key}
          className="flex items-center justify-between gap-4 font-mono"
          style={{
            fontSize: "12px",
            color: "var(--treatment-ink)",
            fontVariantNumeric: "tabular-nums",
          }}
        >
          <span style={{ color: "var(--treatment-muted)" }}>{series.short}</span>
          <span style={{ fontWeight: series.emphasis === "thesis" ? 600 : 400 }}>
            {formatTamBillion(point[series.key])}
          </span>
        </span>
      ))}
    </div>
  );
}

function drawChart(
  canvas: HTMLCanvasElement,
  width: number,
  height: number,
  points: readonly TamProjectionPoint[],
  colors: SeriesColors,
  hover: number | null,
) {
  const ctx = canvas.getContext("2d");
  if (!ctx) return;
  const dpr = window.devicePixelRatio || 1;
  canvas.width = Math.round(width * dpr);
  canvas.height = Math.round(height * dpr);
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, width, height);

  const count = points.length;
  const last = points[count - 1];
  const plotW = width - PAD.left - PAD.right;
  const plotH = height - PAD.top - PAD.bottom;
  if (plotW <= 0 || plotH <= 0 || !last) return;

  const rawMax = last.defaultSoftwareCompanyTamBillion;
  const x = scaleLinear()
    .domain([0, count - 1])
    .range([PAD.left, PAD.left + plotW]);
  const y = scaleLinear()
    .domain([0, rawMax])
    .nice(5)
    .range([PAD.top + plotH, PAD.top]);

  // Horizontal gridlines + y tick labels.
  ctx.font = AXIS_FONT;
  ctx.textBaseline = "middle";
  ctx.textAlign = "right";
  for (const tick of y.ticks(5)) {
    const yy = y(tick);
    ctx.strokeStyle = colors.grid;
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(PAD.left, yy);
    ctx.lineTo(PAD.left + plotW, yy);
    ctx.stroke();
    ctx.fillStyle = colors.axis;
    ctx.fillText(formatTamBillion(tick), PAD.left - 8, yy);
  }

  // X tick labels at the start point and each year's Q4.
  ctx.textAlign = "center";
  ctx.textBaseline = "top";
  points.forEach((point, index) => {
    if (index !== 0 && !point.quarter.endsWith("Q4")) return;
    ctx.fillStyle = colors.axis;
    ctx.fillText(formatQuarterShort(point.quarter), x(index), PAD.top + plotH + 8);
  });

  // Series lines (drawn base -> thesis so the dark thesis line sits on top).
  for (const series of TAM_SERIES) {
    const color = colors[series.key];
    const generator: Line<TamProjectionPoint> = d3line<TamProjectionPoint>()
      .x((_d, index) => x(index))
      .y((d) => y(d[series.key]))
      .context(ctx);
    ctx.beginPath();
    generator(points);
    ctx.strokeStyle = color;
    ctx.lineWidth = lineWeight(series.emphasis);
    ctx.lineJoin = "round";
    ctx.stroke();
  }

  drawEndpointLabels(ctx, x, y, points, colors);
  drawLegend(ctx, colors);

  // Hover crosshair + per-series markers.
  if (hover !== null && hover >= 0 && hover < count) {
    const hx = x(hover);
    ctx.strokeStyle = colors.axis;
    ctx.lineWidth = 1;
    ctx.setLineDash([3, 3]);
    ctx.beginPath();
    ctx.moveTo(hx, PAD.top);
    ctx.lineTo(hx, PAD.top + plotH);
    ctx.stroke();
    ctx.setLineDash([]);
    const hovered = points[hover];
    if (hovered) {
      for (const series of TAM_SERIES) {
        const hy = y(hovered[series.key]);
        ctx.beginPath();
        ctx.arc(hx, hy, series.emphasis === "thesis" ? 4 : 3, 0, Math.PI * 2);
        ctx.fillStyle = colors[series.key];
        ctx.fill();
      }
    }
  }
}

// Endpoint value labels to the right of each line, de-collided so the three
// 2030Q4 values never stack on top of each other. The thesis line gets the one
// bounded Flare accent on the page: a small chartreuse chip behind its value.
function drawEndpointLabels(
  ctx: CanvasRenderingContext2D,
  x: (n: number) => number,
  y: (n: number) => number,
  points: readonly TamProjectionPoint[],
  colors: SeriesColors,
) {
  const lastIndex = points.length - 1;
  const lastPoint = points[lastIndex];
  if (!lastPoint) return;
  const ex = x(lastIndex);
  const labels = TAM_SERIES.map((series) => ({
    series,
    value: lastPoint[series.key],
    yPos: y(lastPoint[series.key]),
    text: formatTamBillion(lastPoint[series.key]),
  })).sort((a, b) => a.yPos - b.yPos);

  // Enforce a minimum vertical gap, pushing labels downward as needed.
  const gap = 15;
  for (let i = 1; i < labels.length; i += 1) {
    const curr = labels[i];
    const prev = labels[i - 1];
    if (curr && prev && curr.yPos - prev.yPos < gap) {
      curr.yPos = prev.yPos + gap;
    }
  }

  ctx.font = AXIS_FONT;
  ctx.textAlign = "left";
  ctx.textBaseline = "middle";
  for (const label of labels) {
    const tx = ex + 8;
    // Endpoint dot in the series colour.
    ctx.beginPath();
    ctx.arc(ex, y(label.value), label.series.emphasis === "thesis" ? 3.5 : 2.5, 0, Math.PI * 2);
    ctx.fillStyle = colors[label.series.key];
    ctx.fill();
    if (label.series.emphasis === "thesis") {
      const width = measureWidth(label.text, AXIS_FONT) + 8;
      ctx.fillStyle = colors.flare;
      ctx.fillRect(tx - 3, label.yPos - 8, width, 16);
      ctx.fillStyle = "#0b0b0b";
      ctx.font = "600 11px 'Geist', system-ui, sans-serif";
    } else {
      ctx.fillStyle = colors[label.series.key];
      ctx.font = AXIS_FONT;
    }
    ctx.fillText(label.text, tx, label.yPos);
  }
}

// In-canvas legend, top-left where the lines are low and the corner is empty.
// pretext wraps each (full) series label to the legend column width without a
// DOM reflow, so the longest name degrades gracefully on a narrow chart.
function drawLegend(ctx: CanvasRenderingContext2D, colors: SeriesColors) {
  const x0 = PAD.left + 8;
  const swatch = 12;
  const lineHeight = 15;
  const maxLabelWidth = 150;
  let yy = PAD.top + 2;

  ctx.font = LEGEND_FONT;
  ctx.textAlign = "left";
  ctx.textBaseline = "top";
  for (const series of TAM_SERIES) {
    ctx.strokeStyle = colors[series.key];
    ctx.lineWidth = lineWeight(series.emphasis);
    ctx.beginPath();
    ctx.moveTo(x0, yy + 7);
    ctx.lineTo(x0 + swatch, yy + 7);
    ctx.stroke();

    const prepared = prepareWithSegments(series.label, LEGEND_FONT);
    const { lines } = layoutWithLines(prepared, maxLabelWidth, lineHeight);
    ctx.fillStyle = colors.axis;
    for (const layoutLine of lines) {
      ctx.fillText(layoutLine.text, x0 + swatch + 8, yy);
      yy += lineHeight;
    }
    yy += 4;
  }
}

function measureWidth(text: string, font: string): number {
  const prepared = prepareWithSegments(text, font);
  const { lines } = layoutWithLines(prepared, Number.POSITIVE_INFINITY, 16);
  const first = lines[0];
  return first ? Math.ceil(first.width) : 0;
}

"use client";

import { scaleLinear } from "d3-scale";
import { area as d3area, line as d3line } from "d3-shape";
import { layoutWithLines, prepareWithSegments } from "@chenglou/pretext";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  TAM_SERIES,
  WINDOW,
  axisYears,
  formatMonthYear,
  formatTamBillion,
  type TamProjectionPoint,
  type TamSeriesKey,
} from "./model";

// Client-only canvas chart. The endpoint KPIs are the SSR/no-JS reading path;
// this is the progressive enhancement, so it renders nothing until mounted.
// Canvas (not SVG) is deliberate: no static asset, everything recomputes from
// the URL-state value on the client.
//
// Two series sampled weekly (smooth curves): the external standard projection
// and the Guardian projection with enthusiast demand stacked above it.
//
// The chart MORPHS between lever changes — every series value and the y-axis max
// interpolate together (easeOutCubic over ~320ms) so the lines grow into place
// and the axis rescales smoothly. prefers-reduced-motion snaps instead.

const PAD = { top: 18, right: 84, bottom: 30, left: 48 } as const;
const LEGEND_FONT = "12px 'Geist', system-ui, sans-serif";
const AXIS_FONT = "11px 'Geist', system-ui, sans-serif";
const MORPH_MS = 320;

// Newsroom treatment palette (the article is always the Argent/newsroom ground,
// so these are constant). The thesis band is the one bounded Flare moment.
const COLORS = {
  thesisStroke: "#0b0b0b",
  referenceStroke: "rgba(11, 11, 11, 0.42)",
  bandFill: "rgba(204, 255, 0, 0.32)", // Flare #ccff00
  bandHatch: "rgba(11, 11, 11, 0.12)",
  flare: "#ccff00",
  grid: "rgba(11, 11, 11, 0.10)",
  axis: "rgba(11, 11, 11, 0.55)",
} as const;

function strokeFor(key: TamSeriesKey): string {
  if (key === "guardianProjectionTamBillion") return COLORS.thesisStroke;
  return COLORS.referenceStroke;
}

function lineWeight(emphasis: "reference" | "thesis"): number {
  if (emphasis === "thesis") return 5;
  return 2.5;
}

interface RenderState {
  readonly points: readonly TamProjectionPoint[];
  readonly yMax: number;
}

function prefersReducedMotion(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

// Target y-axis max: the highest series rounded up to a nice gridline value.
function niceYMax(points: readonly TamProjectionPoint[]): number {
  const last = points[points.length - 1];
  const rawMax = last ? last.guardianProjectionTamBillion : 0;
  const domain = scaleLinear().domain([0, rawMax]).nice(5).domain();
  return domain[1] ?? rawMax;
}

function lerp(a: number, b: number, t: number): number {
  return a + (b - a) * t;
}

// Interpolate the whole chart state (both series at every point + the y-axis
// max) between renders. Each series stays <= yMax at both endpoints, so the
// interpolated series stay <= the interpolated yMax — no clipping mid-tween.
function lerpState(from: RenderState, to: RenderState, t: number): RenderState {
  const points = to.points.map((tp, i) => {
    const fp = from.points[i] ?? tp;
    return {
      t: tp.t,
      standardProjectionTamBillion: lerp(
        fp.standardProjectionTamBillion,
        tp.standardProjectionTamBillion,
        t,
      ),
      guardianProjectionTamBillion: lerp(
        fp.guardianProjectionTamBillion,
        tp.guardianProjectionTamBillion,
        t,
      ),
      enthusiastDemandBillion: lerp(fp.enthusiastDemandBillion, tp.enthusiastDemandBillion, t),
    };
  });
  return { points, yMax: lerp(from.yMax, to.yMax, t) };
}

export function TamChart({ points }: { points: readonly TamProjectionPoint[] }) {
  const hostRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [mounted, setMounted] = useState(false);
  const [size, setSize] = useState({ width: 0, height: 0 });
  const [hover, setHover] = useState<number | null>(null);

  const renderRef = useRef<RenderState | null>(null);
  const rafRef = useRef<number | null>(null);
  const hoverRef = useRef<number | null>(null);
  hoverRef.current = hover;

  useEffect(() => setMounted(true), []);

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

  const paint = useCallback(
    (state: RenderState) => {
      const canvas = canvasRef.current;
      const host = hostRef.current;
      if (!canvas || !host || size.width === 0 || size.height === 0) return;
      drawChart(canvas, size.width, size.height, state, hoverRef.current);
    },
    [size],
  );
  const paintRef = useRef(paint);
  paintRef.current = paint;

  // Morph from the on-screen state to the new target when the data changes.
  useEffect(() => {
    if (!mounted) return;
    const target: RenderState = { points, yMax: niceYMax(points) };
    const from = renderRef.current;
    if (!from || prefersReducedMotion()) {
      renderRef.current = target;
      paintRef.current(target);
      return;
    }
    if (rafRef.current !== null) cancelAnimationFrame(rafRef.current);
    let startTs: number | null = null;
    const step = (ts: number) => {
      if (startTs === null) startTs = ts;
      const t = Math.min(1, (ts - startTs) / MORPH_MS);
      const eased = 1 - Math.pow(1 - t, 3); // easeOutCubic
      const state = lerpState(from, target, eased);
      renderRef.current = state;
      paintRef.current(state);
      if (t < 1) {
        rafRef.current = requestAnimationFrame(step);
      } else {
        renderRef.current = target;
        rafRef.current = null;
      }
    };
    rafRef.current = requestAnimationFrame(step);
    return () => {
      if (rafRef.current !== null) cancelAnimationFrame(rafRef.current);
    };
  }, [points, mounted]);

  // Resize and hover never animate — just repaint the current state.
  useEffect(() => {
    if (!mounted || !renderRef.current) return;
    paint(renderRef.current);
  }, [size, hover, mounted, paint]);

  if (!mounted) {
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
          aria-label="Projected TAM, weekly from 2026 to 2030: a dashed Standard Projection reference line and a Guardian Projection line, with Enthusiast Demand shaded between."
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
        {formatMonthYear(point.t)}
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

// Diagonal hatch tiles, created once on the client and reused as fill patterns.
let bandHatchTile: HTMLCanvasElement | null = null;

function hatchTile(stroke: string): HTMLCanvasElement {
  const tile = document.createElement("canvas");
  tile.width = 7;
  tile.height = 7;
  const c = tile.getContext("2d");
  if (c) {
    c.strokeStyle = stroke;
    c.lineWidth = 1;
    c.beginPath();
    c.moveTo(0, 7);
    c.lineTo(7, 0);
    c.stroke();
  }
  return tile;
}

function drawChart(
  canvas: HTMLCanvasElement,
  width: number,
  height: number,
  state: RenderState,
  hover: number | null,
) {
  const { points, yMax } = state;
  const ctx = canvas.getContext("2d");
  if (!ctx) return;
  const dpr = window.devicePixelRatio || 1;
  canvas.width = Math.round(width * dpr);
  canvas.height = Math.round(height * dpr);
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, width, height);

  const count = points.length;
  const plotW = width - PAD.left - PAD.right;
  const plotH = height - PAD.top - PAD.bottom;
  if (plotW <= 0 || plotH <= 0 || count === 0) return;
  const plotBottom = PAD.top + plotH;

  const x = scaleLinear()
    .domain([WINDOW.startT, WINDOW.endT])
    .range([PAD.left, PAD.left + plotW]);
  const y = scaleLinear().domain([0, yMax]).range([plotBottom, PAD.top]);

  // Horizontal gridlines + y tick labels.
  ctx.font = AXIS_FONT;
  ctx.textBaseline = "middle";
  ctx.textAlign = "right";
  for (const tick of y.ticks(5)) {
    const yy = y(tick);
    ctx.strokeStyle = COLORS.grid;
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(PAD.left, yy);
    ctx.lineTo(PAD.left + plotW, yy);
    ctx.stroke();
    ctx.fillStyle = COLORS.axis;
    ctx.fillText(formatTamBillion(tick), PAD.left - 8, yy);
  }

  // X axis: a label (and faint rule) at each calendar year inside the window.
  // First/last labels align inward so the edge years don't bleed off the plot.
  ctx.textBaseline = "top";
  const years = axisYears();
  const rightEdge = PAD.left + plotW;
  years.forEach((year, idx) => {
    const px = x(year);
    ctx.strokeStyle = COLORS.grid;
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(px, PAD.top);
    ctx.lineTo(px, plotBottom);
    ctx.stroke();
    const atStart = idx === 0;
    const atEnd = idx === years.length - 1;
    ctx.fillStyle = COLORS.axis;
    ctx.textAlign = atStart ? "left" : atEnd ? "right" : "center";
    ctx.fillText(String(year), atStart ? PAD.left : atEnd ? rightEdge : px, plotBottom + 8);
  });

  // Fill the enthusiast demand band between the two projections.
  if (!bandHatchTile) bandHatchTile = hatchTile(COLORS.bandHatch);

  const bandArea = d3area<TamProjectionPoint>()
    .x((d) => x(d.t))
    .y0((d) => y(d.standardProjectionTamBillion))
    .y1((d) => y(d.guardianProjectionTamBillion))
    .context(ctx);

  const fillArea = (generator: typeof bandArea, wash: string, tile: HTMLCanvasElement) => {
    ctx.beginPath();
    generator(points);
    ctx.fillStyle = wash;
    ctx.fill();
    const pattern = ctx.createPattern(tile, "repeat");
    if (pattern) {
      ctx.fillStyle = pattern;
      ctx.fill();
    }
  };

  fillArea(bandArea, COLORS.bandFill, bandHatchTile);

  // Series strokes — thick with rounded joins; the cloud reference line is
  // dashed so it reads as external context, not a Guardian projection.
  for (const series of TAM_SERIES) {
    const generator = d3line<TamProjectionPoint>()
      .x((d) => x(d.t))
      .y((d) => y(d[series.key]))
      .context(ctx);
    ctx.beginPath();
    generator(points);
    ctx.strokeStyle = strokeFor(series.key);
    ctx.lineWidth = lineWeight(series.emphasis);
    ctx.lineJoin = "round";
    ctx.lineCap = "round";
    ctx.setLineDash(series.emphasis === "reference" ? [7, 5] : []);
    ctx.stroke();
  }
  ctx.setLineDash([]);

  drawEndpointLabels(ctx, x, y, points);
  drawDemandBrace(ctx, x, y, points);
  drawLegend(ctx);

  // Hover crosshair + per-series markers.
  if (hover !== null && hover >= 0 && hover < count) {
    const hovered = points[hover];
    if (hovered) {
      const hx = x(hovered.t);
      ctx.strokeStyle = COLORS.axis;
      ctx.lineWidth = 1;
      ctx.setLineDash([3, 3]);
      ctx.beginPath();
      ctx.moveTo(hx, PAD.top);
      ctx.lineTo(hx, plotBottom);
      ctx.stroke();
      ctx.setLineDash([]);
      for (const series of TAM_SERIES) {
        const hy = y(hovered[series.key]);
        ctx.beginPath();
        ctx.arc(hx, hy, series.emphasis === "thesis" ? 5 : 4, 0, Math.PI * 2);
        ctx.fillStyle = strokeFor(series.key);
        ctx.fill();
      }
    }
  }
}

function drawDemandBrace(
  ctx: CanvasRenderingContext2D,
  x: (n: number) => number,
  y: (n: number) => number,
  points: readonly TamProjectionPoint[],
) {
  const lastPoint = points[points.length - 1];
  if (!lastPoint) return;
  const top = y(lastPoint.guardianProjectionTamBillion);
  const bottom = y(lastPoint.standardProjectionTamBillion);
  if (!Number.isFinite(top) || !Number.isFinite(bottom) || bottom - top < 28) return;

  const bx = x(lastPoint.t) + 46;
  const mid = (top + bottom) / 2;
  const hook = 11;
  const waist = 7;

  ctx.save();
  ctx.strokeStyle = COLORS.thesisStroke;
  ctx.lineWidth = 1.4;
  ctx.lineCap = "round";
  ctx.lineJoin = "round";
  ctx.beginPath();
  ctx.moveTo(bx + hook, top);
  ctx.bezierCurveTo(bx, top, bx, mid - waist, bx + hook, mid);
  ctx.bezierCurveTo(bx, mid + waist, bx, bottom, bx + hook, bottom);
  ctx.stroke();

  ctx.translate(bx + hook + 11, mid);
  ctx.rotate(-Math.PI / 2);
  ctx.font = "600 11px 'Geist', system-ui, sans-serif";
  ctx.fillStyle = COLORS.thesisStroke;
  ctx.textAlign = "center";
  ctx.textBaseline = "middle";
  ctx.fillText("Enthusiast Demand", 0, 0);
  ctx.restore();
}

// Endpoint value labels to the right of each line, de-collided. The thesis line
// gets the one bounded Flare accent: a chartreuse chip behind its value.
function drawEndpointLabels(
  ctx: CanvasRenderingContext2D,
  x: (n: number) => number,
  y: (n: number) => number,
  points: readonly TamProjectionPoint[],
) {
  const lastPoint = points[points.length - 1];
  if (!lastPoint) return;
  const ex = x(lastPoint.t);
  const labels = TAM_SERIES.map((series) => ({
    series,
    value: lastPoint[series.key],
    yPos: y(lastPoint[series.key]),
    text: formatTamBillion(lastPoint[series.key]),
  })).sort((a, b) => a.yPos - b.yPos);

  const gap = 16;
  for (let i = 1; i < labels.length; i += 1) {
    const curr = labels[i];
    const prev = labels[i - 1];
    if (curr && prev && curr.yPos - prev.yPos < gap) {
      curr.yPos = prev.yPos + gap;
    }
  }

  ctx.textAlign = "left";
  ctx.textBaseline = "middle";
  for (const label of labels) {
    const tx = ex + 9;
    const thesis = label.series.emphasis === "thesis";
    ctx.beginPath();
    ctx.arc(ex, y(label.value), thesis ? 4 : 3, 0, Math.PI * 2);
    ctx.fillStyle = strokeFor(label.series.key);
    ctx.fill();
    if (thesis) {
      const w = measureWidth(label.text, "600 11px 'Geist', system-ui, sans-serif") + 8;
      ctx.fillStyle = COLORS.flare;
      ctx.fillRect(tx - 3, label.yPos - 8, w, 16);
      ctx.fillStyle = "#0b0b0b";
      ctx.font = "600 11px 'Geist', system-ui, sans-serif";
    } else {
      ctx.fillStyle = strokeFor(label.series.key);
      ctx.font = AXIS_FONT;
    }
    ctx.fillText(label.text, tx, label.yPos);
  }
}

// In-canvas legend, top-left where the lines are low and the corner is empty.
function drawLegend(ctx: CanvasRenderingContext2D) {
  const x0 = PAD.left + 8;
  const swatch = 16;
  const lineHeight = 15;
  const maxLabelWidth = 170;
  let yy = PAD.top + 2;

  ctx.font = LEGEND_FONT;
  ctx.textAlign = "left";
  ctx.textBaseline = "top";
  for (const series of TAM_SERIES) {
    ctx.strokeStyle = strokeFor(series.key);
    ctx.lineWidth = lineWeight(series.emphasis);
    ctx.lineCap = "round";
    ctx.beginPath();
    ctx.moveTo(x0, yy + 7);
    ctx.lineTo(x0 + swatch, yy + 7);
    ctx.stroke();

    const prepared = prepareWithSegments(series.label, LEGEND_FONT);
    const { lines } = layoutWithLines(prepared, maxLabelWidth, lineHeight);
    ctx.fillStyle = COLORS.axis;
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

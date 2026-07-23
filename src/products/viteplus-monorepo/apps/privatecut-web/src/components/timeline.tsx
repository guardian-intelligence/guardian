import { useCallback, useEffect, useRef, useState } from "react";
import type { ThumbnailTile } from "~/engine/client";
import { MAX_SELECTION_SECONDS } from "~/engine/limits";
import type { ProbeSummary, SelectionRange } from "~/engine/types";
import { formatClock } from "~/lib/format";

export interface TimelineProps {
  readonly summary: ProbeSummary;
  readonly tiles: readonly ThumbnailTile[];
  readonly tileCount: number;
  readonly selection: SelectionRange;
  readonly snapToKeyframes: boolean;
  readonly onSelectionChange: (selection: SelectionRange) => void;
}

type DragTarget = "start" | "end" | "window" | null;

const MIN_SELECTION_S = 0.5;

// The selection strip: filmstrip thumbnails, keyframe ticks, two drag
// handles clamped to the 60s cap, and a draggable window between them.
export function Timeline({
  summary,
  tiles,
  tileCount,
  selection,
  snapToKeyframes,
  onSelectionChange,
}: TimelineProps) {
  const stripRef = useRef<HTMLDivElement>(null);
  const [drag, setDrag] = useState<DragTarget>(null);
  const dragOffsetRef = useRef(0);

  const duration = summary.durationS;
  const toFraction = (s: number) => Math.min(Math.max(s / duration, 0), 1);

  const snap = useCallback(
    (t: number): number => {
      if (!snapToKeyframes || summary.keyframesS.length === 0) return t;
      let best = t;
      let bestDist = Number.POSITIVE_INFINITY;
      for (const k of summary.keyframesS) {
        const d = Math.abs(k - t);
        if (d < bestDist) {
          bestDist = d;
          best = k;
        }
      }
      // Snap only when a keyframe is within reach; far ticks should not yank
      // the handle across the strip.
      return bestDist <= Math.max(duration * 0.02, 0.5) ? best : t;
    },
    [snapToKeyframes, summary.keyframesS, duration],
  );

  const timeAtPointer = useCallback(
    (clientX: number): number => {
      const strip = stripRef.current;
      if (!strip) return 0;
      const rect = strip.getBoundingClientRect();
      const fraction = Math.min(Math.max((clientX - rect.left) / rect.width, 0), 1);
      return fraction * duration;
    },
    [duration],
  );

  useEffect(() => {
    if (drag === null) return;
    const onMove = (e: PointerEvent) => {
      const t = timeAtPointer(e.clientX);
      if (drag === "start") {
        const startS = snap(Math.min(t, selection.endS - MIN_SELECTION_S));
        const clampedStart = Math.max(startS, selection.endS - MAX_SELECTION_SECONDS);
        onSelectionChange({ startS: Math.max(clampedStart, 0), endS: selection.endS });
      } else if (drag === "end") {
        const endS = Math.max(t, selection.startS + MIN_SELECTION_S);
        const clampedEnd = Math.min(endS, selection.startS + MAX_SELECTION_SECONDS, duration);
        onSelectionChange({ startS: selection.startS, endS: clampedEnd });
      } else {
        const width = selection.endS - selection.startS;
        let startS = snap(t - dragOffsetRef.current);
        startS = Math.min(Math.max(startS, 0), duration - width);
        onSelectionChange({ startS, endS: startS + width });
      }
    };
    const onUp = () => setDrag(null);
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    return () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
    };
  }, [drag, selection, duration, snap, timeAtPointer, onSelectionChange]);

  const startPct = toFraction(selection.startS) * 100;
  const endPct = toFraction(selection.endS) * 100;

  return (
    <div className="select-none">
      <div
        ref={stripRef}
        className="relative h-20 overflow-hidden rounded-xl border border-line bg-night-raised"
      >
        <Filmstrip tiles={tiles} tileCount={tileCount} />
        {summary.keyframesS.length > 1 && summary.keyframesS.length <= 400 && (
          <div className="pointer-events-none absolute inset-x-0 bottom-0 h-2">
            {summary.keyframesS.map((k) => (
              <span
                key={k}
                className="absolute bottom-0 h-2 w-px bg-glow-teal/50"
                style={{ left: `${toFraction(k) * 100}%` }}
              />
            ))}
          </div>
        )}
        <div
          className="absolute inset-y-0 bg-night/70"
          style={{ left: 0, width: `${startPct}%` }}
        />
        <div
          className="absolute inset-y-0 right-0 bg-night/70"
          style={{ width: `${100 - endPct}%` }}
        />
        <div
          role="presentation"
          className="absolute inset-y-0 cursor-grab border-y-2 border-glow-warm/70 active:cursor-grabbing"
          style={{ left: `${startPct}%`, width: `${endPct - startPct}%` }}
          onPointerDown={(e) => {
            dragOffsetRef.current = timeAtPointer(e.clientX) - selection.startS;
            setDrag("window");
          }}
        />
        <Handle side="start" pct={startPct} onGrab={() => setDrag("start")} />
        <Handle side="end" pct={endPct} onGrab={() => setDrag("end")} />
      </div>
      <div className="mt-2 flex justify-between font-mono text-xs text-mist-faint">
        <span>{formatClock(0)}</span>
        <span className="text-mist-dim">
          {formatClock(selection.startS)} – {formatClock(selection.endS)}
        </span>
        <span>{formatClock(duration)}</span>
      </div>
    </div>
  );
}

function Handle({ side, pct, onGrab }: { side: "start" | "end"; pct: number; onGrab: () => void }) {
  return (
    <div
      role="slider"
      aria-label={side === "start" ? "Selection start" : "Selection end"}
      aria-valuenow={Math.round(pct)}
      tabIndex={0}
      className="absolute inset-y-0 z-10 w-3 -translate-x-1/2 cursor-ew-resize touch-none"
      style={{ left: `${pct}%` }}
      onPointerDown={(e) => {
        e.stopPropagation();
        onGrab();
      }}
    >
      <div className="mx-auto h-full w-1.5 rounded-full bg-glow-warm shadow-[0_0_12px_rgba(255,237,216,0.45)]" />
    </div>
  );
}

function Filmstrip({ tiles, tileCount }: { tiles: readonly ThumbnailTile[]; tileCount: number }) {
  const canvasRef = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas || tiles.length === 0) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    const tileWidth = canvas.width / tileCount;
    for (const tile of tiles) {
      const scale = canvas.height / tile.bitmap.height;
      ctx.drawImage(
        tile.bitmap,
        tile.index * tileWidth,
        0,
        Math.max(tile.bitmap.width * scale, tileWidth),
        canvas.height,
      );
    }
  }, [tiles, tileCount]);

  return (
    <canvas
      ref={canvasRef}
      width={tileCount * 120}
      height={80}
      className="absolute inset-0 h-full w-full opacity-80"
    />
  );
}

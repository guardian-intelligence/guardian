import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { EncodeResult, ShorttyEngine, ThumbnailTile } from "~/engine/client";
import { estimateSelection } from "~/engine/estimate";
import { MAX_SELECTION_SECONDS } from "~/engine/limits";
import type { ProbeSummary, SelectionRange } from "~/engine/types";
import { emitSpan } from "~/lib/telemetry/browser";
import { formatBitrate, formatSeconds } from "~/lib/format";
import { ResultCard } from "./result-card";
import { Timeline } from "./timeline";

const TILE_COUNT = 16;

export interface EditorProps {
  readonly engine: ShorttyEngine;
  readonly file: File;
  readonly summary: ProbeSummary;
  readonly onReset: () => void;
}

type Phase =
  | { kind: "selecting" }
  | { kind: "encoding"; pass: number; fraction: number }
  | { kind: "done"; result: EncodeResult }
  | { kind: "failed"; message: string };

export function Editor({ engine, file, summary, onReset }: EditorProps) {
  const [selection, setSelection] = useState<SelectionRange>(() => ({
    startS: 0,
    endS: Math.min(summary.durationS, MAX_SELECTION_SECONDS),
  }));
  const [snapToKeyframes, setSnapToKeyframes] = useState(true);
  const [tiles, setTiles] = useState<readonly ThumbnailTile[]>([]);
  const [phase, setPhase] = useState<Phase>({ kind: "selecting" });
  const videoRef = useRef<HTMLVideoElement>(null);

  const previewUrl = useMemo(() => URL.createObjectURL(file), [file]);
  useEffect(() => () => URL.revokeObjectURL(previewUrl), [previewUrl]);

  useEffect(() => {
    let collected: ThumbnailTile[] = [];
    void engine.thumbnails(TILE_COUNT, 80, (tile) => {
      collected = [...collected, tile];
      setTiles(collected);
    });
  }, [engine]);

  // Keep the preview inside the selection: seek to the start when it moves.
  useEffect(() => {
    const video = videoRef.current;
    if (video && Math.abs(video.currentTime - selection.startS) > 0.25) {
      video.currentTime = selection.startS;
    }
  }, [selection.startS]);

  const estimate = useMemo(() => estimateSelection(summary, selection), [summary, selection]);

  const encode = useCallback(() => {
    setPhase({ kind: "encoding", pass: 1, fraction: 0 });
    emitSpan("shortty.encode_started", {
      duration_s: estimate.durationS.toFixed(2),
      video_bps: String(Math.round(estimate.videoBitsPerSecond)),
    });
    const startedAt = performance.now();
    engine
      .encode(selection, (pass, fraction) => setPhase({ kind: "encoding", pass, fraction }))
      .then((result) => {
        const { outcome } = result;
        emitSpan("shortty.encode_completed", {
          mode: outcome.mode,
          bytes: String(outcome.bytes),
          utilization: outcome.utilization.toFixed(4),
          passes: String(outcome.passes.length),
          duration_s: outcome.durationS.toFixed(2),
          height: String(outcome.height),
          frame_rate: String(Math.round(outcome.frameRate)),
          wall_ms: String(Math.round(outcome.wallMs)),
          source_codec: summary.video.codec,
        });
        setPhase({ kind: "done", result });
      })
      .catch((error: unknown) => {
        emitSpan("shortty.encode_failed", {
          message: error instanceof Error ? error.message : String(error),
          wall_ms: String(Math.round(performance.now() - startedAt)),
        });
        setPhase({
          kind: "failed",
          message: error instanceof Error ? error.message : "Encoding failed.",
        });
      });
  }, [engine, selection, estimate, summary.video.codec]);

  if (phase.kind === "done") {
    return (
      <ResultCard
        result={phase.result}
        sourceName={file.name}
        onBack={() => setPhase({ kind: "selecting" })}
      />
    );
  }

  return (
    <div className="glass-strong space-y-6 p-6">
      {/* eslint-disable-next-line jsx-a11y/media-has-caption */}
      <video
        ref={videoRef}
        src={previewUrl}
        controls
        playsInline
        className="max-h-[42vh] w-full rounded-xl bg-black"
      />
      <Timeline
        summary={summary}
        tiles={tiles}
        tileCount={TILE_COUNT}
        selection={selection}
        snapToKeyframes={snapToKeyframes}
        onSelectionChange={(next) => {
          if (phase.kind === "selecting") setSelection(next);
        }}
      />
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div className="font-mono text-sm text-mist-dim">
          <span className="text-mist">{formatSeconds(estimate.durationS)}</span> selected
          {" → "}
          {estimate.likelyRemux ? (
            <span className="text-glow-warm">original quality, no re-encode</span>
          ) : (
            <>
              <span className="text-mist">{formatBitrate(estimate.videoBitsPerSecond)}</span>
              {" → "}
              <span className="text-mist">
                {estimate.label}
                {estimate.frameRate < summary.video.frameRate - 1
                  ? ` ${Math.round(estimate.frameRate)}fps`
                  : ""}
              </span>
            </>
          )}
        </div>
        <label className="flex cursor-pointer items-center gap-2 text-sm text-mist-faint">
          <input
            type="checkbox"
            checked={snapToKeyframes}
            onChange={(e) => setSnapToKeyframes(e.currentTarget.checked)}
            className="accent-[#7d6bff]"
          />
          snap to keyframes
        </label>
      </div>
      {phase.kind === "failed" && (
        <p className="rounded-xl border border-red-400/30 bg-red-400/10 px-4 py-3 text-sm text-red-200">
          {phase.message}
        </p>
      )}
      <div className="flex flex-wrap items-center gap-3">
        <button
          type="button"
          disabled={phase.kind === "encoding"}
          onClick={encode}
          className="rounded-xl bg-mist px-8 py-3 font-medium text-night transition-transform hover:scale-[1.02] disabled:opacity-60"
        >
          {phase.kind === "encoding" ? (
            <EncodingLabel pass={phase.pass} fraction={phase.fraction} />
          ) : (
            "Create clip"
          )}
        </button>
        <button
          type="button"
          onClick={onReset}
          className="rounded-xl border border-line-strong px-6 py-3 text-mist-dim transition-colors hover:text-mist"
        >
          Different video
        </button>
      </div>
      {phase.kind === "encoding" && (
        <div className="h-1.5 overflow-hidden rounded-full bg-white/[0.06]">
          <div
            className="h-full rounded-full bg-gradient-to-r from-glow-violet to-glow-teal transition-[width] duration-200"
            style={{ width: `${Math.round(phase.fraction * 100)}%` }}
          />
        </div>
      )}
    </div>
  );
}

function EncodingLabel({ pass, fraction }: { pass: number; fraction: number }) {
  const label = pass === 1 ? "Encoding" : `Pass ${pass} — dialing in quality`;
  return (
    <span>
      {label} {Math.round(fraction * 100)}%
    </span>
  );
}

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { EncodeResult, PrivateCutEngine, ThumbnailTile } from "~/engine/client";
import { estimateSelection } from "~/engine/estimate";
import type { SizeLimitBytes } from "~/engine/limits";
import {
  DEFAULT_SIZE_LIMIT_BYTES,
  MAX_SELECTION_SECONDS,
  SIZE_LIMIT_PRESETS_BYTES,
} from "~/engine/limits";
import type { MediaSource, ProbeSummary, SelectionRange } from "~/engine/types";
import { emitSpan } from "~/lib/telemetry/browser";
import { formatBitrate, formatSeconds } from "~/lib/format";
import { ResultCard } from "./result-card";
import { Timeline } from "./timeline";

const TILE_COUNT = 16;

export interface EditorProps {
  readonly engine: PrivateCutEngine;
  readonly source: MediaSource;
  readonly summary: ProbeSummary;
  readonly onReset: () => void;
}

type Phase =
  | { kind: "selecting" }
  | { kind: "encoding"; pass: number; fraction: number }
  | { kind: "done"; result: EncodeResult }
  | { kind: "failed"; message: string };

export function Editor({ engine, source, summary, onReset }: EditorProps) {
  const [selection, setSelection] = useState<SelectionRange>(() => ({
    startS: 0,
    endS: Math.min(summary.durationS, MAX_SELECTION_SECONDS),
  }));
  const [snapToKeyframes, setSnapToKeyframes] = useState(true);
  const [limitBytes, setLimitBytes] = useState<SizeLimitBytes>(DEFAULT_SIZE_LIMIT_BYTES);
  const [tiles, setTiles] = useState<readonly ThumbnailTile[]>([]);
  const [phase, setPhase] = useState<Phase>({ kind: "selecting" });
  const videoRef = useRef<HTMLVideoElement>(null);

  // A remote source's CDN URL feeds the <video> element directly (media
  // playback needs no CORS); a dropped file gets an object URL.
  const previewUrl = useMemo(
    () => (source instanceof File ? URL.createObjectURL(source) : source.url),
    [source],
  );
  useEffect(() => {
    if (!(source instanceof File)) return undefined;
    return () => URL.revokeObjectURL(previewUrl);
  }, [source, previewUrl]);

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

  const estimate = useMemo(
    () => estimateSelection(summary, selection, limitBytes),
    [summary, selection, limitBytes],
  );

  // Portrait footage should hug a narrow column so the media, timeline, and
  // controls read as one stack — never a sliver of video floating in black.
  const portrait = summary.video.height >= summary.video.width;

  const encode = useCallback(() => {
    setPhase({ kind: "encoding", pass: 1, fraction: 0 });
    emitSpan("privatecut.encode_started", {
      duration_s: estimate.durationS.toFixed(2),
      video_bps: String(Math.round(estimate.videoBitsPerSecond)),
      limit_bytes: String(limitBytes),
    });
    const startedAt = performance.now();
    engine
      .encode(selection, limitBytes, (pass, fraction) =>
        setPhase({ kind: "encoding", pass, fraction }),
      )
      .then((result) => {
        const { outcome } = result;
        emitSpan("privatecut.encode_completed", {
          mode: outcome.mode,
          bytes: String(outcome.bytes),
          limit_bytes: String(outcome.limitBytes),
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
        emitSpan("privatecut.encode_failed", {
          message: error instanceof Error ? error.message : String(error),
          wall_ms: String(Math.round(performance.now() - startedAt)),
        });
        setPhase({
          kind: "failed",
          message: error instanceof Error ? error.message : "Encoding failed.",
        });
      });
  }, [engine, selection, limitBytes, estimate, summary.video.codec]);

  if (phase.kind === "done") {
    return (
      <ResultCard
        result={phase.result}
        sourceName={source.name}
        onBack={() => setPhase({ kind: "selecting" })}
      />
    );
  }

  return (
    <div
      className="glass-strong mx-auto w-full space-y-6 p-6"
      data-illumination-glass="strong"
      style={{ maxWidth: portrait ? "30rem" : "52rem" }}
    >
      {/* eslint-disable-next-line jsx-a11y/media-has-caption */}
      <video
        ref={videoRef}
        src={previewUrl}
        controls
        playsInline
        className="mx-auto block max-h-[62vh] max-w-full rounded-xl bg-black"
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
      <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Size cap">
        <span className="font-mono text-sm text-mist-faint">size cap</span>
        {SIZE_LIMIT_PRESETS_BYTES.map((preset) => (
          <button
            key={preset}
            type="button"
            aria-pressed={preset === limitBytes}
            disabled={phase.kind === "encoding"}
            onClick={() => setLimitBytes(preset)}
            className={
              preset === limitBytes
                ? "rounded-full bg-mist px-3 py-1 font-mono text-sm text-night disabled:opacity-60"
                : "rounded-full border border-line-strong px-3 py-1 font-mono text-sm text-mist-dim transition-colors hover:text-mist disabled:opacity-60"
            }
          >
            {preset / 1_000_000} MB
          </button>
        ))}
      </div>
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

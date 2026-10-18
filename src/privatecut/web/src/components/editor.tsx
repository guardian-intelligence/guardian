import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import type { EncodeResult, PrivateCutEngine, ThumbnailTile } from "~/engine/client";
import { estimateSelection } from "~/engine/estimate";
import { OUTPUT_CONTAINER_INFO, OUTPUT_CONTAINERS } from "~/engine/formats";
import type { SizeLimitBytes } from "~/engine/limits";
import {
  DEFAULT_SIZE_LIMIT_BYTES,
  MAX_SELECTION_SECONDS,
  SIZE_LIMIT_PRESETS_BYTES,
} from "~/engine/limits";
import type {
  FrameSize,
  MediaSource,
  OutputContainer,
  ProbeSummary,
  SelectionRange,
} from "~/engine/types";
import { emitSpan, newTraceIdHex } from "@guardian/telemetry";
import { finishEncodeRecord, noteEncodeProgress, startEncodeRecord } from "~/lib/encode-journal";
import { describeEncodeFailure, failureAttrs, passTrail } from "~/lib/encode-report";
import { formatBitrate, formatSeconds } from "~/lib/format";
import { useObjectUrl } from "~/lib/object-url";
import { ResultCard } from "./result-card";
import { Timeline } from "./timeline";

const TILE_COUNT = 16;

// The started attributes a failure event repeats so it reads on its own;
// the rest join through trace.id. Event props ship as one JSON string the
// beacon drops whole past 2000 characters, and a failure also carries its
// stack and pass trail.
const FAILURE_CONTEXT = [
  "trace.id",
  "duration_s",
  "limit_bytes",
  "output_format",
  "output_codec",
  "planned_audio_bps",
  "planned_audio_channels",
  "source_codec",
  "source_width",
  "source_height",
] as const;

function pickAttrs(attrs: Record<string, string>, keys: readonly string[]): Record<string, string> {
  const picked: Record<string, string> = {};
  for (const key of keys) {
    const value = attrs[key];
    if (value !== undefined) picked[key] = value;
  }
  return picked;
}

export interface EditorProps {
  readonly engine: PrivateCutEngine;
  readonly source: MediaSource;
  readonly summary: ProbeSummary;
  readonly onReset: () => void;
}

type Phase =
  | { kind: "selecting" }
  | { kind: "encoding"; pass: number; fraction: number; frame: FrameSize | null }
  | { kind: "done"; result: EncodeResult }
  // `ref` is the encode's trace id prefix: what a person quotes back to us
  // to find their failure in the events table.
  | { kind: "failed"; message: string; ref: string };

export function Editor({ engine, source, summary, onReset }: EditorProps) {
  const [selection, setSelection] = useState<SelectionRange>(() => ({
    startS: 0,
    endS: Math.min(summary.durationS, MAX_SELECTION_SECONDS),
  }));
  const [snapToKeyframes, setSnapToKeyframes] = useState(true);
  const [limitBytes, setLimitBytes] = useState<SizeLimitBytes>(DEFAULT_SIZE_LIMIT_BYTES);
  const [outputFormat, setOutputFormat] = useState<OutputContainer>(
    () => summary.outputProfiles.find((profile) => profile.format === "mp4")?.format ?? "webm",
  );
  const [tiles, setTiles] = useState<readonly ThumbnailTile[]>([]);
  const [phase, setPhase] = useState<Phase>({ kind: "selecting" });
  const videoRef = useRef<HTMLVideoElement>(null);

  // A remote source's CDN URL feeds the <video> element directly (media
  // playback needs no CORS); a dropped file gets an object URL.
  const fileUrl = useObjectUrl(source instanceof File ? source : null);
  const previewUrl = source instanceof File ? fileUrl : source.url;

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

  const outputProfile =
    summary.outputProfiles.find((profile) => profile.format === outputFormat) ??
    summary.outputProfiles[0];
  if (outputProfile === undefined) {
    throw new Error("No supported output format.");
  }
  const estimate = useMemo(
    () => estimateSelection(summary, selection, outputProfile, limitBytes),
    [summary, selection, outputProfile, limitBytes],
  );

  // Portrait footage should hug a narrow column so the media, timeline, and
  // controls read as one stack — never a sliver of video floating in black.
  const portrait = summary.video.height >= summary.video.width;

  const encode = useCallback(() => {
    setPhase({ kind: "encoding", pass: 1, fraction: 0, frame: null });
    const encodeId = newTraceIdHex();
    const startedAttrs: Record<string, string> = {
      "trace.id": encodeId,
      duration_s: estimate.durationS.toFixed(2),
      selection_start_s: selection.startS.toFixed(2),
      selection_end_s: selection.endS.toFixed(2),
      video_bps: String(Math.round(estimate.videoBitsPerSecond)),
      planned_short_side: String(estimate.shortSide),
      planned_frame_rate: String(Math.round(estimate.frameRate)),
      planned_audio_bps: String(estimate.audio?.bitrate ?? 0),
      planned_audio_channels: String(estimate.audio?.numberOfChannels ?? 0),
      likely_remux: String(estimate.likelyRemux),
      limit_bytes: String(limitBytes),
      output_format: outputFormat,
      output_codec: outputProfile.videoCodec,
      output_audio_codec: outputProfile.audioCodec ?? "",
      source_codec: summary.video.codec,
      source_container: summary.container,
      source_width: String(summary.video.width),
      source_height: String(summary.video.height),
      source_frame_rate: summary.video.frameRate.toFixed(2),
      source_duration_s: summary.durationS.toFixed(2),
      source_audio_codec: summary.audioCodec ?? "",
      video_decode_mode: summary.videoDecodeMode,
      hardware_concurrency: String(navigator.hardwareConcurrency ?? 0),
    };
    emitSpan("privatecut.encode_started", startedAttrs);
    startEncodeRecord(encodeId, startedAttrs);
    const startedAt = performance.now();
    engine
      .encode(selection, limitBytes, outputFormat, {
        onProgress: (pass, fraction, frame) => {
          noteEncodeProgress(pass, fraction);
          setPhase({ kind: "encoding", pass, fraction, frame });
        },
        onPass: (report) =>
          emitSpan("privatecut.encode_pass", {
            "trace.id": encodeId,
            pass: String(report.pass),
            requested_bps: String(Math.round(report.videoBitsPerSecond)),
            bytes: String(report.bytes),
            limit_bytes: String(limitBytes),
            over_cap: String(report.bytes > limitBytes),
            width: String(report.frame.width),
            height: String(report.frame.height),
            frame_rate: String(Math.round(report.frame.frameRate)),
            wall_ms: String(Math.round(report.wallMs)),
          }),
      })
      .then((result) => {
        finishEncodeRecord();
        const { outcome } = result;
        emitSpan("privatecut.encode_completed", {
          "trace.id": encodeId,
          mode: outcome.mode,
          bytes: String(outcome.bytes),
          limit_bytes: String(outcome.limitBytes),
          utilization: outcome.utilization.toFixed(4),
          passes: String(outcome.passes.length),
          pass_trail: passTrail(outcome.passes),
          duration_s: outcome.durationS.toFixed(2),
          width: String(outcome.width),
          height: String(outcome.height),
          frame_rate: String(Math.round(outcome.frameRate)),
          audio_bps: String(outcome.audio?.bitrate ?? 0),
          audio_channels: String(outcome.audio?.numberOfChannels ?? 0),
          wall_ms: String(Math.round(outcome.wallMs)),
          source_codec: summary.video.codec,
          source_container: summary.container,
          output_format: outcome.outputFormat,
          output_codec: outcome.codec,
        });
        setPhase({ kind: "done", result });
      })
      .catch((error: unknown) => {
        finishEncodeRecord();
        emitSpan("privatecut.encode_failed", {
          ...pickAttrs(startedAttrs, FAILURE_CONTEXT),
          ...failureAttrs(error),
          wall_ms: String(Math.round(performance.now() - startedAt)),
        });
        setPhase({
          kind: "failed",
          message: describeEncodeFailure(error, limitBytes, outputFormat),
          ref: encodeId.slice(0, 8),
        });
      });
  }, [engine, selection, limitBytes, outputFormat, outputProfile, estimate, summary]);

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
        src={previewUrl ?? undefined}
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
          // Only a running encode freezes the selection; after a failure the
          // way forward is usually a shorter one.
          if (phase.kind !== "encoding") setSelection(next);
        }}
      />
      <div className="rounded-2xl border border-line bg-white/[0.025] p-4">
        <div className="grid gap-4 sm:grid-cols-[minmax(0,0.8fr)_minmax(0,1.2fr)]">
          <fieldset className="min-w-0" aria-label="Output format">
            <legend className="mb-2 text-xs font-medium uppercase tracking-[0.16em] text-mist-faint">
              Format
            </legend>
            <div className="grid grid-cols-2 gap-1 rounded-xl border border-line bg-night/60 p-1">
              {OUTPUT_CONTAINERS.map((format) => {
                const available = summary.outputProfiles.some(
                  (profile) => profile.format === format,
                );
                const info = OUTPUT_CONTAINER_INFO[format];
                return (
                  <button
                    key={format}
                    type="button"
                    aria-pressed={format === outputFormat}
                    onClick={() => {
                      if (phase.kind === "encoding") {
                        toast.info("Your clip is already being created", {
                          description: "Export settings can be changed after this encode finishes.",
                        });
                        return;
                      }
                      if (available) {
                        setOutputFormat(format);
                        return;
                      }
                      toast.warning(`${info.label} isn't available in this browser`, {
                        description:
                          format === "mp4"
                            ? "Firefox isn't exposing an H.264/AAC encoder. Export WebM here, or open PrivateCut in Chrome to create MP4."
                            : "This browser isn't exposing a compatible WebM encoder. Choose MP4 or open PrivateCut in Chrome.",
                      });
                    }}
                    className={
                      format === outputFormat
                        ? "rounded-lg bg-mist px-3 py-2 text-sm font-medium text-night shadow-sm"
                        : available
                          ? "rounded-lg px-3 py-2 text-sm text-mist-dim transition-colors hover:bg-white/[0.04] hover:text-mist"
                          : "rounded-lg px-3 py-2 text-sm text-mist-faint transition-colors hover:bg-white/[0.04] hover:text-mist"
                    }
                  >
                    {info.label}
                  </button>
                );
              })}
            </div>
          </fieldset>
          <fieldset className="min-w-0" aria-label="Size cap">
            <legend className="mb-2 text-xs font-medium uppercase tracking-[0.16em] text-mist-faint">
              Maximum size
            </legend>
            <div className="grid grid-cols-4 gap-1 rounded-xl border border-line bg-night/60 p-1">
              {SIZE_LIMIT_PRESETS_BYTES.map((preset) => (
                <button
                  key={preset}
                  type="button"
                  aria-pressed={preset === limitBytes}
                  onClick={() => {
                    if (phase.kind === "encoding") {
                      toast.info("Your clip is already being created", {
                        description: "The size cap can be changed after this encode finishes.",
                      });
                      return;
                    }
                    setLimitBytes(preset);
                  }}
                  className={
                    preset === limitBytes
                      ? "rounded-lg bg-mist px-2 py-2 text-sm font-medium text-night shadow-sm"
                      : "rounded-lg px-2 py-2 text-sm text-mist-dim transition-colors hover:bg-white/[0.04] hover:text-mist"
                  }
                >
                  {preset / 1_000_000}
                  <span className="ml-1 text-[0.7rem] opacity-70">MB</span>
                </button>
              ))}
            </div>
          </fieldset>
        </div>
        {estimate.lowQuality && (
          <div
            role="status"
            className="mt-4 rounded-xl border border-glow-warm/30 bg-glow-warm/[0.08] px-4 py-3"
          >
            <p className="text-sm font-medium text-glow-warm">Low-quality output likely</p>
            <p className="mt-1 text-sm text-mist-dim">
              Try increasing maximum size or splitting into shorter clips.
            </p>
          </div>
        )}
        <div className="mt-4 flex flex-wrap items-center justify-between gap-3 border-t border-line pt-4">
          <div className="font-mono text-xs text-mist-dim sm:text-sm">
            <span className="text-mist">{formatSeconds(estimate.durationS)}</span> selected
            {" · "}
            {estimate.likelyRemux ? (
              <span className="text-glow-warm">original quality, no re-encode</span>
            ) : (
              <>
                <span className="text-mist">{formatBitrate(estimate.videoBitsPerSecond)}</span>
                {" · "}
                <span className="text-mist">
                  {estimate.label}
                  {estimate.frameRate < summary.video.frameRate - 1
                    ? ` ${Math.round(estimate.frameRate)}fps`
                    : ""}
                </span>
              </>
            )}
          </div>
          <label className="flex cursor-pointer items-center gap-2 text-xs text-mist-faint sm:text-sm">
            <input
              type="checkbox"
              checked={snapToKeyframes}
              onChange={(e) => setSnapToKeyframes(e.currentTarget.checked)}
              className="accent-[#7d6bff]"
            />
            snap to keyframes
          </label>
        </div>
      </div>
      {phase.kind === "failed" && (
        <p className="rounded-xl border border-red-400/30 bg-red-400/10 px-4 py-3 text-sm text-red-200">
          {phase.message}
          <span className="ml-2 font-mono text-xs text-red-200/60">ref {phase.ref}</span>
        </p>
      )}
      <div className="grid grid-cols-[minmax(0,1fr)_auto] gap-3">
        <button
          type="button"
          onClick={() => {
            if (phase.kind === "encoding") {
              toast.info("Your clip is already being created", {
                description: "Keep this tab open until the current encode finishes.",
              });
              return;
            }
            encode();
          }}
          className="rounded-xl bg-mist px-8 py-3 font-medium text-night transition-transform hover:scale-[1.01]"
        >
          {phase.kind === "encoding" ? (
            <EncodingLabel pass={phase.pass} fraction={phase.fraction} frame={phase.frame} />
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

function EncodingLabel({
  pass,
  fraction,
  frame,
}: {
  pass: number;
  fraction: number;
  frame: FrameSize | null;
}) {
  // Later passes either re-aim the bitrate or step the frame down to get
  // under the cap; naming the frame shows which.
  const label =
    pass === 1
      ? "Encoding"
      : frame === null
        ? `Pass ${pass} — dialing in quality`
        : `Pass ${pass} — ${Math.min(frame.width, frame.height)}p · ${Math.round(frame.frameRate)} fps`;
  return (
    <span>
      {label} {Math.round(fraction * 100)}%
    </span>
  );
}

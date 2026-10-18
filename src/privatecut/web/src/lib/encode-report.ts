// How an encode reads to two audiences: telemetry attributes precise enough
// to diagnose a failure from production events alone, and a sentence the
// person who hit it can act on.

import { EngineError } from "~/engine/client";
import { OUTPUT_CONTAINER_INFO } from "~/engine/formats";
import type { EncodePass, OutputContainer } from "~/engine/types";
import { formatBytes } from "./format";

// Compact per-pass trail: `pass:requested_bps:bytes:WxH@fps`, `;`-joined.
// Event props travel as one JSON string capped at 2000 characters, so the
// trail stays a fraction of that even at the pass limit.
export function passTrail(passes: readonly EncodePass[]): string {
  return passes
    .map(
      (p) =>
        `${p.pass}:${Math.round(p.videoBitsPerSecond)}:${p.bytes}:` +
        `${p.frame.width}x${p.frame.height}@${Math.round(p.frame.frameRate)}`,
    )
    .join(";");
}

export function failureAttrs(error: unknown): Record<string, string> {
  if (error instanceof EngineError) {
    return {
      kind: error.kind,
      "error.type": error.causeName,
      message: error.message.slice(0, 240),
      stage: error.stage ?? "",
      passes: String(error.passes.length),
      pass_trail: passTrail(error.passes),
      "error.stack": error.stack?.slice(0, 500) ?? "",
    };
  }
  const cause = error instanceof Error ? error : new Error(String(error));
  return {
    kind: "failed",
    "error.type": cause.name,
    message: cause.message.slice(0, 240),
    "error.stack": cause.stack?.slice(0, 500) ?? "",
  };
}

export function describeEncodeFailure(
  error: unknown,
  limitBytes: number,
  outputFormat: OutputContainer,
): string {
  const other = outputFormat === "mp4" ? "WebM" : "MP4";
  if (error instanceof EngineError) {
    if (error.kind === "stalled") {
      return `Your browser's video encoder stopped responding. Try again, or switch to ${other} if it keeps happening.`;
    }
    if (error.kind === "worker-lost") {
      return "The encoder stopped unexpectedly. Reload the page and try again.";
    }
    if (error.causeName === "ConvergenceError") {
      return `This browser can't fit the selection under ${formatBytes(limitBytes)} at a watchable quality. Shorten it or choose a larger maximum size.`;
    }
    if (error.causeName === "EncodingError") {
      return `Your browser's ${OUTPUT_CONTAINER_INFO[outputFormat].label} encoder failed. Try again, or switch to ${other}.`;
    }
  }
  return error instanceof Error ? error.message : "Encoding failed.";
}

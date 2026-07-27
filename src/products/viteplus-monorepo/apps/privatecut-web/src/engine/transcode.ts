import { BufferTarget, Conversion, Output } from "mediabunny";
import type { BudgetPlan } from "./budget";
import type { FramePlan } from "./ladder";
import { createOutputFormat } from "./output";
import type { OpenedInput } from "./probe";
import type { OutputProfile, SelectionRange } from "./types";

export interface TranscodePassInput {
  readonly opened: OpenedInput;
  readonly selection: SelectionRange;
  readonly frame: FramePlan;
  readonly budget: BudgetPlan;
  readonly profile: OutputProfile;
  readonly videoBitsPerSecond: number;
  readonly onProgress?: ((fraction: number) => void) | undefined;
}

export interface TranscodePassOutcome {
  readonly buffer: ArrayBuffer;
  readonly bytes: number;
}

// One encode pass at an exact requested video bitrate. MP4 uses H.264/AAC for
// broad ingest compatibility; WebM uses the best browser-encodable WebM
// video/audio profile resolved during probing.
export async function transcodePass(input: TranscodePassInput): Promise<TranscodePassOutcome> {
  const { opened, selection, frame, budget, profile, videoBitsPerSecond, onProgress } = input;
  const target = new BufferTarget();
  const output = new Output({
    format: createOutputFormat(profile.format),
    target,
  });
  const conversion = await Conversion.init({
    input: opened.input,
    output,
    tracks: "primary",
    trim: { start: selection.startS, end: selection.endS },
    video: {
      codec: profile.videoCodec,
      width: frame.width,
      height: frame.height,
      fit: "fill",
      frameRate: frame.frameRate,
      bitrate: Math.round(videoBitsPerSecond),
      forceTranscode: true,
    },
    audio:
      budget.audio === null
        ? { discard: true }
        : {
            codec: profile.audioCodec ?? "aac",
            bitrate: budget.audio.bitrate,
            numberOfChannels: budget.audio.numberOfChannels,
            forceTranscode: true,
          },
    showWarnings: false,
  });
  if (!conversion.isValid) {
    const reasons = conversion.discardedTracks
      .map((t) => `${t.track.type}: ${t.reason}`)
      .join("; ");
    throw new Error(`This video cannot be converted (${reasons || "unknown reason"}).`);
  }
  if (onProgress) {
    conversion.onProgress = (progress) => onProgress(progress);
  }
  await conversion.execute();
  const buffer = target.buffer;
  if (buffer === null) {
    throw new Error("Conversion finished without producing a file.");
  }
  return { buffer, bytes: buffer.byteLength };
}

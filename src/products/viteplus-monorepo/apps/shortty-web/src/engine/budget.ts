import { SIZE_LIMIT_BYTES } from "./limits";

// Byte budget for a selection: what the video track may spend after the
// container and audio take their cut. All estimates here steer the FIRST
// encode pass only — acceptance is decided by measuring the finalized file
// (convergence.ts), never by these numbers.

export interface AudioPlan {
  readonly bitrate: number;
  readonly numberOfChannels: 1 | 2;
}

export interface BudgetPlan {
  readonly limitBytes: number;
  readonly containerBytes: number;
  readonly audio: AudioPlan | null;
  readonly audioBytes: number;
  readonly videoBitsPerSecond: number;
}

// MP4 sample-table overhead is billed per sample: stsz/stts/stco/ctts entries
// plus fragmentation slack. ~16 bytes per video frame and ~12 per AAC frame
// (48 kHz ⇒ ~46.9 frames/s) with a fixed allowance for ftyp/moov headers.
export function estimateContainerBytes(
  durationS: number,
  frameRate: number,
  hasAudio: boolean,
): number {
  const videoSamples = Math.ceil(durationS * frameRate);
  const audioSamples = hasAudio ? Math.ceil(durationS * 46.9) : 0;
  return 4096 + videoSamples * 16 + audioSamples * 12;
}

// Audio gets at most ~15% of the total bit budget, on the 128k→96k→64k AAC
// ladder, dropping to mono only at the bottom. Below 32k equivalent the
// track costs more than it is worth and is dropped entirely.
export function planAudio(
  durationS: number,
  limitBytes: number,
  sourceHasAudio: boolean,
): AudioPlan | null {
  if (!sourceHasAudio || durationS <= 0) return null;
  const totalBps = (limitBytes * 8) / durationS;
  const share = totalBps * 0.15;
  if (share >= 128_000) return { bitrate: 128_000, numberOfChannels: 2 };
  if (share >= 96_000) return { bitrate: 96_000, numberOfChannels: 2 };
  if (share >= 64_000) return { bitrate: 64_000, numberOfChannels: 2 };
  if (share >= 32_000) return { bitrate: 48_000, numberOfChannels: 1 };
  return null;
}

export interface BudgetInput {
  readonly durationS: number;
  readonly frameRate: number;
  readonly sourceHasAudio: boolean;
  // Source video bitrate, if known: the video budget never exceeds it — we
  // will not spend more bits than the source carries.
  readonly sourceVideoBitsPerSecond?: number | undefined;
  readonly limitBytes?: number;
}

export function planBudget(input: BudgetInput): BudgetPlan {
  const limitBytes = input.limitBytes ?? SIZE_LIMIT_BYTES;
  const durationS = Math.max(input.durationS, 0.1);
  const audio = planAudio(durationS, limitBytes, input.sourceHasAudio);
  const containerBytes = estimateContainerBytes(durationS, input.frameRate, audio !== null);
  const audioBytes = audio ? Math.ceil((audio.bitrate * durationS) / 8) : 0;
  const videoBytes = Math.max(limitBytes - containerBytes - audioBytes, 0);
  let videoBitsPerSecond = (videoBytes * 8) / durationS;
  if (
    input.sourceVideoBitsPerSecond !== undefined &&
    input.sourceVideoBitsPerSecond > 0 &&
    videoBitsPerSecond > input.sourceVideoBitsPerSecond
  ) {
    videoBitsPerSecond = input.sourceVideoBitsPerSecond;
  }
  return { limitBytes, containerBytes, audio, audioBytes, videoBitsPerSecond };
}

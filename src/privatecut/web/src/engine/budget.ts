// Byte budget for a selection: what the video track may spend after the
// container and audio take their cut. All estimates here steer the FIRST
// encode pass only — acceptance is decided by measuring the finalized file
// (convergence.ts), never by these numbers.

import type { AudioCodecChoice } from "./types";

export interface AudioPlan {
  readonly bitrate: number;
  readonly numberOfChannels: 1 | 2;
  // Pinned output rate. Set only where the encoder's safe bitrate range was
  // measured at a specific rate (AAC); otherwise the source rate passes through.
  readonly sampleRate?: number;
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

// Audio rungs per codec, richest first. AAC's ladder is bounded by the
// platform encoder, not by us: Chrome on macOS runs AAC through AudioToolbox
// inside the GPU process, and a bitrate under its applicable range kills the
// whole GPU process (exit code 5) — the video decoder, every WebGL context,
// and the page's media die with it — while isConfigSupported still answers
// true. Measured 2026-10-18 at 44.1 and 48 kHz: stereo fails at 64k and
// holds from 72k; mono fails at 40k and holds from 48k. So AAC never goes
// stereo below 96k, and it is pinned to 48 kHz, the rate those numbers were
// taken at. Opus runs in-process (libopus) and takes the full ladder.
const AAC_SAMPLE_RATE = 48_000;

const AUDIO_RUNGS: Record<AudioCodecChoice, readonly AudioPlan[]> = {
  aac: [
    { bitrate: 128_000, numberOfChannels: 2, sampleRate: AAC_SAMPLE_RATE },
    { bitrate: 96_000, numberOfChannels: 2, sampleRate: AAC_SAMPLE_RATE },
    { bitrate: 64_000, numberOfChannels: 1, sampleRate: AAC_SAMPLE_RATE },
    { bitrate: 48_000, numberOfChannels: 1, sampleRate: AAC_SAMPLE_RATE },
  ],
  opus: [
    { bitrate: 128_000, numberOfChannels: 2 },
    { bitrate: 96_000, numberOfChannels: 2 },
    { bitrate: 64_000, numberOfChannels: 2 },
    { bitrate: 48_000, numberOfChannels: 1 },
  ],
  vorbis: [
    { bitrate: 128_000, numberOfChannels: 2 },
    { bitrate: 96_000, numberOfChannels: 2 },
    { bitrate: 64_000, numberOfChannels: 2 },
    { bitrate: 48_000, numberOfChannels: 1 },
  ],
};

// Audio gets at most ~15% of the total bit budget: the richest rung that
// fits that share, or the codec's bottom rung down to a 32k share. Below
// that the track costs more than it is worth and is dropped entirely. An
// unknown codec gets the AAC ladder — the one with the hard floor.
export function planAudio(
  durationS: number,
  limitBytes: number,
  sourceHasAudio: boolean,
  codec: AudioCodecChoice | null = "aac",
): AudioPlan | null {
  if (!sourceHasAudio || durationS <= 0) return null;
  const share = ((limitBytes * 8) / durationS) * 0.15;
  const rungs = AUDIO_RUNGS[codec ?? "aac"];
  const fitting = rungs.find((rung) => rung.bitrate <= share);
  if (fitting !== undefined) return fitting;
  return share >= 32_000 ? (rungs[rungs.length - 1] ?? null) : null;
}

export interface BudgetInput {
  readonly durationS: number;
  readonly frameRate: number;
  readonly sourceHasAudio: boolean;
  readonly audioCodec?: AudioCodecChoice | null;
  // Source video bitrate, if known: the video budget never exceeds it — we
  // will not spend more bits than the source carries.
  readonly sourceVideoBitsPerSecond?: number | undefined;
  readonly limitBytes: number;
}

export function planBudget(input: BudgetInput): BudgetPlan {
  const { limitBytes } = input;
  const durationS = Math.max(input.durationS, 0.1);
  const audio = planAudio(durationS, limitBytes, input.sourceHasAudio, input.audioCodec);
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

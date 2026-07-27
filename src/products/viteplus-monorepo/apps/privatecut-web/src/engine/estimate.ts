// Live quality estimate for the selection readout. Pure math over the probe
// summary — safe to run on the main thread on every drag frame, no worker
// round-trip. The numbers steer expectations only; the encode itself is
// governed by the measured acceptance gate.

import { planBudget } from "./budget";
import { canRemuxCodecs } from "./formats";
import { planFrame } from "./ladder";
import type { OutputProfile, ProbeSummary, SelectionRange } from "./types";

export interface SelectionEstimate {
  readonly durationS: number;
  readonly videoBitsPerSecond: number;
  readonly label: string;
  readonly frameRate: number;
  readonly likelyRemux: boolean;
}

export function estimateSelection(
  summary: ProbeSummary,
  selection: SelectionRange,
  profile: OutputProfile,
  limitBytes: number,
): SelectionEstimate {
  const durationS = Math.max(selection.endS - selection.startS, 0.1);
  const budget = planBudget({
    durationS,
    frameRate: summary.video.frameRate,
    sourceHasAudio: summary.hasAudio,
    sourceVideoBitsPerSecond: summary.video.bitsPerSecond,
    limitBytes,
  });
  const frame = planFrame({
    sourceWidth: summary.video.width,
    sourceHeight: summary.video.height,
    sourceFrameRate: summary.video.frameRate,
    videoBitsPerSecond: budget.videoBitsPerSecond,
    codec: profile.videoCodec,
  });
  const sourceBps = summary.video.bitsPerSecond ?? Number.POSITIVE_INFINITY;
  const onKeyframe = summary.keyframesS.some((t) => Math.abs(t - selection.startS) <= 0.05);
  const remuxCompatible =
    (!summary.hasAudio || summary.audioCodec !== null) &&
    canRemuxCodecs(profile.format, summary.video.codec, summary.audioCodec);
  const likelyRemux =
    remuxCompatible && onKeyframe && (sourceBps / 8) * durationS < limitBytes * 0.9;
  return {
    durationS,
    videoBitsPerSecond: budget.videoBitsPerSecond,
    label: frame.isSource ? "original size" : frame.label,
    frameRate: frame.frameRate,
    likelyRemux,
  };
}

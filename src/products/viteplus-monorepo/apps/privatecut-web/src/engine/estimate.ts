// Live quality estimate for the selection readout. Pure math over the probe
// summary — safe to run on the main thread on every drag frame, no worker
// round-trip. The numbers steer expectations only; the encode itself is
// governed by the measured acceptance gate.

import { planBudget } from "./budget";
import { planFrame } from "./ladder";
import { SIZE_LIMIT_BYTES } from "./limits";
import type { ProbeSummary, SelectionRange } from "./types";

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
): SelectionEstimate {
  const durationS = Math.max(selection.endS - selection.startS, 0.1);
  const budget = planBudget({
    durationS,
    frameRate: summary.video.frameRate,
    sourceHasAudio: summary.hasAudio,
    sourceVideoBitsPerSecond: summary.video.bitsPerSecond,
  });
  const frame = planFrame({
    sourceWidth: summary.video.width,
    sourceHeight: summary.video.height,
    sourceFrameRate: summary.video.frameRate,
    videoBitsPerSecond: budget.videoBitsPerSecond,
    codec: "avc",
  });
  const sourceBps = summary.video.bitsPerSecond ?? Number.POSITIVE_INFINITY;
  const onKeyframe = summary.keyframesS.some((t) => Math.abs(t - selection.startS) <= 0.05);
  const likelyRemux = onKeyframe && (sourceBps / 8) * durationS < SIZE_LIMIT_BYTES * 0.9;
  return {
    durationS,
    videoBitsPerSecond: budget.videoBitsPerSecond,
    label: frame.isSource ? "original size" : frame.label,
    frameRate: frame.frameRate,
    likelyRemux,
  };
}

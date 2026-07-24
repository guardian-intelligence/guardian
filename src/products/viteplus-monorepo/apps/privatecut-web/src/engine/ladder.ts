// Resolution/frame-rate ladder: given the video bit budget, pick the largest
// frame that still gets enough bits per pixel to look sharp. Stepping down a
// rung beats smearing a big frame with too few bits.

export interface Rung {
  readonly height: number;
  readonly label: string;
}

const RUNGS: readonly Rung[] = [
  { height: 2160, label: "4K" },
  { height: 1440, label: "1440p" },
  { height: 1080, label: "1080p" },
  { height: 720, label: "720p" },
  { height: 540, label: "540p" },
  { height: 360, label: "360p" },
];

// Bits-per-pixel floor for acceptable H.264 output; better codecs hold
// quality at lower bpp so their floor scales down.
const BPP_FLOOR = 0.026;

export const CODEC_EFFICIENCY: Record<VideoCodecChoice, number> = {
  avc: 1.0,
  hevc: 0.72,
  av1: 0.62,
};

export type VideoCodecChoice = "avc" | "hevc" | "av1";

export interface FramePlan {
  readonly width: number;
  readonly height: number;
  readonly frameRate: number;
  readonly label: string;
  // True when the plan simply keeps the source frame untouched.
  readonly isSource: boolean;
}

function even(n: number): number {
  return Math.max(2, 2 * Math.round(n / 2));
}

export interface FramePlanInput {
  readonly sourceWidth: number;
  readonly sourceHeight: number;
  readonly sourceFrameRate: number;
  readonly videoBitsPerSecond: number;
  readonly codec: VideoCodecChoice;
}

export function planFrame(input: FramePlanInput): FramePlan {
  const { sourceWidth, sourceHeight, sourceFrameRate, videoBitsPerSecond, codec } = input;
  const floor = BPP_FLOOR * CODEC_EFFICIENCY[codec];
  const aspect = sourceWidth / sourceHeight;
  const shortSide = Math.min(sourceWidth, sourceHeight);

  const candidates: FramePlan[] = [];
  for (const rung of RUNGS) {
    if (rung.height > shortSide) continue;
    const height =
      sourceHeight <= sourceWidth ? rung.height : even(rung.height * (sourceHeight / sourceWidth));
    const width = sourceHeight <= sourceWidth ? even(rung.height * aspect) : rung.height;
    const isSource = height >= sourceHeight && width >= sourceWidth;
    // Halve high frame rates before dropping below 720p: motion smoothness
    // is worth less than legibility at social-post sizes.
    for (const frameRate of sourceFrameRate > 40
      ? [sourceFrameRate, sourceFrameRate / 2]
      : [sourceFrameRate]) {
      candidates.push({
        width: isSource ? sourceWidth : width,
        height: isSource ? sourceHeight : height,
        frameRate,
        label: rung.label,
        isSource: isSource && frameRate === sourceFrameRate,
      });
    }
  }
  if (candidates.length === 0) {
    return {
      width: sourceWidth,
      height: sourceHeight,
      frameRate: sourceFrameRate,
      label: `${Math.min(sourceWidth, sourceHeight)}p`,
      isSource: true,
    };
  }

  for (const plan of candidates) {
    const bpp = videoBitsPerSecond / (plan.width * plan.height * plan.frameRate);
    if (bpp >= floor) return plan;
  }
  // Nothing meets the floor: take the smallest, slowest candidate — the
  // best legibility the budget can buy.
  const last = candidates[candidates.length - 1];
  return (
    last ?? {
      width: sourceWidth,
      height: sourceHeight,
      frameRate: sourceFrameRate,
      label: `${Math.min(sourceWidth, sourceHeight)}p`,
      isSource: true,
    }
  );
}

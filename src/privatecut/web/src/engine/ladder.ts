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
  vp8: 0.9,
  vp9: 0.7,
  av1: 0.62,
};

export type VideoCodecChoice = "avc" | "hevc" | "vp8" | "vp9" | "av1";

export interface FramePlan {
  readonly width: number;
  readonly height: number;
  readonly frameRate: number;
  readonly label: string;
  // True when the plan simply keeps the source frame untouched.
  readonly isSource: boolean;
  readonly meetsQualityFloor: boolean;
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

// The rung's frame for this source: its short side at the rung height,
// aspect preserved, or the untouched source frame once the rung reaches it.
function rungSize(
  rung: Rung,
  sourceWidth: number,
  sourceHeight: number,
): { width: number; height: number; isSource: boolean } {
  const height =
    sourceHeight <= sourceWidth ? rung.height : even(rung.height * (sourceHeight / sourceWidth));
  const width =
    sourceHeight <= sourceWidth ? even(rung.height * (sourceWidth / sourceHeight)) : rung.height;
  const isSource = height >= sourceHeight && width >= sourceWidth;
  return isSource
    ? { width: sourceWidth, height: sourceHeight, isSource }
    : { width, height, isSource };
}

export function planFrame(input: FramePlanInput): FramePlan {
  const { sourceWidth, sourceHeight, sourceFrameRate, videoBitsPerSecond, codec } = input;
  const floor = BPP_FLOOR * CODEC_EFFICIENCY[codec];
  const shortSide = Math.min(sourceWidth, sourceHeight);

  const candidates: Omit<FramePlan, "meetsQualityFloor">[] = [];
  for (const rung of RUNGS) {
    if (rung.height > shortSide) continue;
    const { width, height, isSource } = rungSize(rung, sourceWidth, sourceHeight);
    // Halve high frame rates before dropping below 720p: motion smoothness
    // is worth less than legibility at social-post sizes.
    for (const frameRate of sourceFrameRate > 40
      ? [sourceFrameRate, sourceFrameRate / 2]
      : [sourceFrameRate]) {
      candidates.push({
        width,
        height,
        frameRate,
        label: rung.label,
        isSource: isSource && frameRate === sourceFrameRate,
      });
    }
  }
  if (candidates.length === 0) {
    const plan = {
      width: sourceWidth,
      height: sourceHeight,
      frameRate: sourceFrameRate,
      label: `${Math.min(sourceWidth, sourceHeight)}p`,
      isSource: true,
    };
    return {
      ...plan,
      meetsQualityFloor: videoBitsPerSecond / (plan.width * plan.height * plan.frameRate) >= floor,
    };
  }

  for (const plan of candidates) {
    const bpp = videoBitsPerSecond / (plan.width * plan.height * plan.frameRate);
    if (bpp >= floor) return { ...plan, meetsQualityFloor: true };
  }
  // Nothing meets the floor: take the smallest, slowest candidate — the
  // best legibility the budget can buy.
  const last = candidates[candidates.length - 1];
  const fallback = last ?? {
    width: sourceWidth,
    height: sourceHeight,
    frameRate: sourceFrameRate,
    label: `${Math.min(sourceWidth, sourceHeight)}p`,
    isSource: true,
  };
  return { ...fallback, meetsQualityFloor: false };
}

// Frame rates below this read as a slideshow; stepping stops halving here.
const MIN_STEP_FRAME_RATE = 12;

// One notch cheaper than `plan`, for when the encoder will not get under the
// cap at the planned frame however few bits are requested (its rate floor
// scales with pixel rate; see convergence.ts). Frame rate goes first — at a
// starved budget halving it beats every resolution step — then the next
// smaller rung at the reduced rate. Null when nothing cheaper exists.
export function stepDownFrame(plan: FramePlan, input: FramePlanInput): FramePlan | null {
  const { sourceWidth, sourceHeight, videoBitsPerSecond, codec } = input;
  const floor = BPP_FLOOR * CODEC_EFFICIENCY[codec];
  const withFloor = (next: Omit<FramePlan, "meetsQualityFloor">): FramePlan => ({
    ...next,
    meetsQualityFloor: videoBitsPerSecond / (next.width * next.height * next.frameRate) >= floor,
  });
  if (plan.frameRate / 2 >= MIN_STEP_FRAME_RATE) {
    return withFloor({ ...plan, frameRate: plan.frameRate / 2, isSource: false });
  }
  const shortSide = Math.min(plan.width, plan.height);
  const rung = RUNGS.find((candidate) => candidate.height < shortSide);
  if (rung === undefined) return null;
  const { width, height } = rungSize(rung, sourceWidth, sourceHeight);
  return withFloor({
    width,
    height,
    frameRate: plan.frameRate,
    label: rung.label,
    isSource: false,
  });
}

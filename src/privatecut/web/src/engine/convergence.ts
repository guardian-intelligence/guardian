import { DEFAULT_SAFETY_MARGIN, MAX_PASSES, MIN_UTILIZATION } from "./limits";

// The acceptance gate. The encoder is an untrusted black box: every pass
// produces a finalized file whose exact byte length we measure, and no file
// is ever accepted above the limit — the invariant lives in this loop, not
// in any bitrate math. A pass that lands over the limit (or wastefully
// under it) reprices the next attempt by the measured ratio; the first pass
// doubles as a perfectly calibrated rate model for this exact content.
//
// Repricing assumes the encoder honors lower requests. Hardware encoders
// have a rate floor per pixel rate and stop listening below it — measured
// on VideoToolbox H.264 at 360p30, requests of 162 → 133 → 107 kbps came
// back 4.68 → 4.84 → 5.01 MB. Once a pass shows that (see `isFloored`), the
// loop steps the frame itself down instead: same budget, fewer pixels per
// second, a lower floor.

export interface PassOutcome<T> {
  readonly bytes: number;
  readonly artifact: T;
}

export interface PassRecord {
  readonly videoBitsPerSecond: number;
  readonly bytes: number;
}

// One finished pass as reported to observers: the rate-model record plus
// the frame it encoded and how long it took.
export interface PassReport<F> extends PassRecord {
  readonly pass: number;
  readonly frame: F;
  readonly wallMs: number;
}

export interface ConvergeInput<T, F = undefined> {
  readonly limitBytes: number;
  // Bytes the video track cannot spend (container estimate + audio plan).
  readonly reservedBytes: number;
  readonly initialVideoBitsPerSecond: number;
  // Hard ceiling on what a retry may request (e.g. the source bitrate).
  readonly maxVideoBitsPerSecond?: number | undefined;
  readonly safetyMargin?: number;
  // The frame the first pass encodes, and the next cheaper one once the
  // encoder proves floored. Without `stepDown` the frame never changes.
  readonly frame?: F;
  readonly stepDown?: ((frame: F) => F | null) | undefined;
  readonly encode: (videoBitsPerSecond: number, pass: number, frame: F) => Promise<PassOutcome<T>>;
  readonly onPass?: ((report: PassReport<F>) => void) | undefined;
}

export interface ConvergeResult<T, F = undefined> {
  readonly artifact: T;
  readonly bytes: number;
  readonly utilization: number;
  readonly frame: F;
  readonly passes: readonly PassReport<F>[];
}

// The encoder failed the size gate in a way no further pass can fix. Carries
// the passes so the caller can report exactly what was tried.
export class ConvergenceError<F> extends Error {
  override readonly name = "ConvergenceError";
  constructor(
    message: string,
    readonly passes: readonly PassReport<F>[],
  ) {
    super(message);
  }
}

export function firstPassBitrate(input: {
  initialVideoBitsPerSecond: number;
  safetyMargin?: number | undefined;
  maxVideoBitsPerSecond?: number | undefined;
}): number {
  const margin = input.safetyMargin ?? DEFAULT_SAFETY_MARGIN;
  const requested = input.initialVideoBitsPerSecond * margin;
  const capped =
    input.maxVideoBitsPerSecond !== undefined
      ? Math.min(requested, input.maxVideoBitsPerSecond)
      : requested;
  return Math.max(Math.floor(capped), 50_000);
}

// Reprice from a measured pass: scale the video budget by how far the video
// payload (total minus reserved) missed its target, with light damping so a
// noisy first measurement cannot orbit the limit.
export function repricedBitrate(
  previous: PassRecord,
  limitBytes: number,
  reservedBytes: number,
  maxVideoBitsPerSecond?: number,
): number {
  const targetVideoBytes = Math.max(limitBytes - reservedBytes, 1);
  const actualVideoBytes = Math.max(previous.bytes - reservedBytes, 1);
  const ratio = targetVideoBytes / actualVideoBytes;
  const damped =
    previous.videoBitsPerSecond * (previous.bytes > limitBytes ? ratio * 0.97 : ratio * 0.99);
  const capped =
    maxVideoBitsPerSecond !== undefined ? Math.min(damped, maxVideoBitsPerSecond) : damped;
  return Math.max(Math.floor(capped), 50_000);
}

// An overshooting pass that asked for at least 5% fewer bits than the pass
// before it on the same frame, yet shed less than half that cut from its
// video payload: the encoder is at its floor and repricing is wasted passes.
export function isFloored(
  previous: PassRecord,
  current: PassRecord,
  limitBytes: number,
  reservedBytes: number,
): boolean {
  if (current.bytes <= limitBytes) return false;
  const requestedCut = 1 - current.videoBitsPerSecond / previous.videoBitsPerSecond;
  if (requestedCut < 0.05) return false;
  const previousVideo = Math.max(previous.bytes - reservedBytes, 1);
  const currentVideo = Math.max(current.bytes - reservedBytes, 1);
  const deliveredCut = 1 - currentVideo / previousVideo;
  return deliveredCut < requestedCut * 0.5;
}

export async function converge<T, F = undefined>(
  input: ConvergeInput<T, F>,
): Promise<ConvergeResult<T, F>> {
  const passes: PassReport<F>[] = [];
  let best: (PassOutcome<T> & { frame: F }) | null = null;
  let frame = input.frame as F;
  let bitrate = firstPassBitrate(input);
  // Passes encoded at the current frame; floor detection compares within it.
  let framePasses: PassRecord[] = [];

  for (let pass = 1; pass <= MAX_PASSES; pass += 1) {
    const startedAt = performance.now();
    const outcome = await input.encode(bitrate, pass, frame);
    const record: PassRecord = { videoBitsPerSecond: bitrate, bytes: outcome.bytes };
    const report: PassReport<F> = {
      ...record,
      pass,
      frame,
      wallMs: performance.now() - startedAt,
    };
    passes.push(report);
    input.onPass?.(report);

    if (outcome.bytes <= input.limitBytes) {
      if (best === null || outcome.bytes > best.bytes) {
        best = { ...outcome, frame };
      }
      const utilization = outcome.bytes / input.limitBytes;
      const atCeiling =
        input.maxVideoBitsPerSecond !== undefined && bitrate >= input.maxVideoBitsPerSecond;
      // Accept: good utilization, or no more bits to spend, or out of passes.
      if (utilization >= MIN_UTILIZATION || atCeiling || pass === MAX_PASSES) break;
    } else if (pass === MAX_PASSES) {
      break;
    }

    const previous = framePasses[framePasses.length - 1];
    framePasses.push(record);
    const next =
      previous !== undefined &&
      input.stepDown !== undefined &&
      isFloored(previous, record, input.limitBytes, input.reservedBytes)
        ? input.stepDown(frame)
        : null;
    if (next !== null) {
      // A fresh frame has a fresh rate model: restart from the planned
      // budget rather than carrying the floored frame's repricing over.
      frame = next;
      framePasses = [];
      bitrate = firstPassBitrate(input);
    } else {
      bitrate = repricedBitrate(
        record,
        input.limitBytes,
        input.reservedBytes,
        input.maxVideoBitsPerSecond,
      );
    }
  }

  if (best === null) {
    // Every pass overshot — repricing and frame steps both exhausted. Refuse
    // rather than ship an oversized file.
    throw new ConvergenceError(
      "privatecut: encoder could not produce a file under the size limit",
      passes,
    );
  }
  // Belt and suspenders: the gate the whole product hangs on.
  if (best.bytes > input.limitBytes) {
    throw new ConvergenceError("privatecut: accepted artifact exceeds the size limit", passes);
  }
  return {
    artifact: best.artifact,
    bytes: best.bytes,
    utilization: best.bytes / input.limitBytes,
    frame: best.frame,
    passes,
  };
}

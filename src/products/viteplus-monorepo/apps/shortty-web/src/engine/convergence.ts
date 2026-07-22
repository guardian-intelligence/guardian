import { DEFAULT_SAFETY_MARGIN, MAX_PASSES, MIN_UTILIZATION } from "./limits";

// The acceptance gate. The encoder is an untrusted black box: every pass
// produces a finalized file whose exact byte length we measure, and no file
// is ever accepted above the limit — the invariant lives in this loop, not
// in any bitrate math. A pass that lands over the limit (or wastefully
// under it) reprices the next attempt by the measured ratio; the first pass
// doubles as a perfectly calibrated rate model for this exact content.

export interface PassOutcome<T> {
  readonly bytes: number;
  readonly artifact: T;
}

export interface PassRecord {
  readonly videoBitsPerSecond: number;
  readonly bytes: number;
}

export interface ConvergeInput<T> {
  readonly limitBytes: number;
  // Bytes the video track cannot spend (container estimate + audio plan).
  readonly reservedBytes: number;
  readonly initialVideoBitsPerSecond: number;
  // Hard ceiling on what a retry may request (e.g. the source bitrate).
  readonly maxVideoBitsPerSecond?: number | undefined;
  readonly safetyMargin?: number;
  readonly encode: (videoBitsPerSecond: number, pass: number) => Promise<PassOutcome<T>>;
}

export interface ConvergeResult<T> {
  readonly artifact: T;
  readonly bytes: number;
  readonly utilization: number;
  readonly passes: readonly PassRecord[];
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

export async function converge<T>(input: ConvergeInput<T>): Promise<ConvergeResult<T>> {
  const passes: PassRecord[] = [];
  let best: (PassOutcome<T> & { videoBitsPerSecond: number }) | null = null;
  let bitrate = firstPassBitrate(input);

  for (let pass = 1; pass <= MAX_PASSES; pass += 1) {
    const outcome = await input.encode(bitrate, pass);
    passes.push({ videoBitsPerSecond: bitrate, bytes: outcome.bytes });

    if (outcome.bytes <= input.limitBytes) {
      if (best === null || outcome.bytes > best.bytes) {
        best = { ...outcome, videoBitsPerSecond: bitrate };
      }
      const utilization = outcome.bytes / input.limitBytes;
      const atCeiling =
        input.maxVideoBitsPerSecond !== undefined && bitrate >= input.maxVideoBitsPerSecond;
      // Accept: good utilization, or no more bits to spend, or out of passes.
      if (utilization >= MIN_UTILIZATION || atCeiling || pass === MAX_PASSES) break;
    } else if (pass === MAX_PASSES) {
      break;
    }
    bitrate = repricedBitrate(
      passes[passes.length - 1] as PassRecord,
      input.limitBytes,
      input.reservedBytes,
      input.maxVideoBitsPerSecond,
    );
  }

  if (best === null) {
    // Every pass overshot — with ratio-scaled retries this requires a
    // pathological encoder. Refuse rather than ship an oversized file.
    throw new Error("shortty: encoder could not produce a file under the size limit");
  }
  // Belt and suspenders: the gate the whole product hangs on.
  if (best.bytes > input.limitBytes) {
    throw new Error("shortty: accepted artifact exceeds the size limit");
  }
  return {
    artifact: best.artifact,
    bytes: best.bytes,
    utilization: best.bytes / input.limitBytes,
    passes,
  };
}

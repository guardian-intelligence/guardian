// The product's one promise: no output ever exceeds the size cap the user
// picked. Caps are SI megabytes in the sense upload validators use (not MiB)
// — the stricter reading, so a file that passes our gate passes theirs
// regardless of which one they meant.
export const SIZE_LIMIT_PRESETS_BYTES = [4_000_000, 10_000_000, 25_000_000, 100_000_000] as const;

export type SizeLimitBytes = (typeof SIZE_LIMIT_PRESETS_BYTES)[number];

export const DEFAULT_SIZE_LIMIT_BYTES: SizeLimitBytes = 4_000_000;

// Worker requests are structured-clone data, so the cap is re-validated at
// the trust boundary rather than assumed from the type.
export function isSizeLimit(bytes: number): bytes is SizeLimitBytes {
  return (SIZE_LIMIT_PRESETS_BYTES as readonly number[]).includes(bytes);
}

export const MAX_SELECTION_SECONDS = 60;

// First encode pass targets margin × the byte budget. Conservative on
// purpose: undershoot costs a little quality, overshoot costs a re-encode
// pass. Tuned over time from privatecut.encode_completed telemetry
// (utilization + pass-count distributions per encoder stratum).
export const DEFAULT_SAFETY_MARGIN = 0.93;

// Accept a pass only when it lands in this utilization band. Below the floor
// we owe the user quality we withheld; another pass converts the headroom.
export const MIN_UTILIZATION = 0.88;

export const MAX_PASSES = 4;

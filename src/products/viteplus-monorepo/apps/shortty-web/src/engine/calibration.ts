// Per-device encoder calibration. A device's encoder bias is stable: if this
// machine's hardware H.264 overshoots requested bitrate by 6% at 1080p, it
// will tomorrow too. Remember the measured actual/requested ratio per
// (codec, rung) and pre-correct the next first pass, so returning users
// converge in one pass before any fleet-level margin tuning ships.

import { DEFAULT_SAFETY_MARGIN } from "./limits";

const STORE_NAME = "shortty_calibration_v1";
const MIN_MARGIN = 0.8;
const MAX_MARGIN = 0.985;

type Store = Record<string, number>;

function readStore(): Store {
  try {
    const raw = localStorage.getItem(STORE_NAME);
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    return typeof parsed === "object" && parsed !== null ? (parsed as Store) : {};
  } catch {
    return {};
  }
}

export function calibrationKey(codec: string, height: number): string {
  return `${codec}:${height}`;
}

export function calibratedMargin(codec: string, height: number): number {
  if (typeof localStorage === "undefined") return DEFAULT_SAFETY_MARGIN;
  const stored = readStore()[calibrationKey(codec, height)];
  if (stored === undefined || !Number.isFinite(stored)) return DEFAULT_SAFETY_MARGIN;
  return Math.min(Math.max(stored, MIN_MARGIN), MAX_MARGIN);
}

// Record how the first pass landed: requestedBits vs the bits the encoder
// actually produced. margin_next = clamp(margin_used × requested/actual),
// blended 50/50 with the previous estimate to smooth content variance.
export function recordCalibration(
  codec: string,
  height: number,
  marginUsed: number,
  requestedBits: number,
  actualBits: number,
): void {
  if (typeof localStorage === "undefined" || requestedBits <= 0 || actualBits <= 0) return;
  const ideal = Math.min(
    Math.max(marginUsed * (requestedBits / actualBits), MIN_MARGIN),
    MAX_MARGIN,
  );
  const store = readStore();
  const key = calibrationKey(codec, height);
  const prior = store[key];
  store[key] = prior !== undefined && Number.isFinite(prior) ? prior * 0.5 + ideal * 0.5 : ideal;
  try {
    localStorage.setItem(STORE_NAME, JSON.stringify(store));
  } catch {
    // storage denied/full: calibration is an optimization, not a dependency
  }
}

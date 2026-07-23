import { parseFormFactors, type FormFactor } from "./form-factors.ts";
import { shorttyTarget } from "./targets/shortty.ts";

export interface TargetConfig {
  name: string;
  criticalSelectors: readonly string[];
  foldTolerancePx: number;
  waitSelector?: string;
}

export interface CanaryConfig {
  targetUrl: string;
  target: TargetConfig;
  formFactors: readonly FormFactor[];
  seekMs: number;
  timeoutMs: number;
}

export const TARGETS: Record<string, TargetConfig> = {
  [shorttyTarget.name]: shorttyTarget,
};

const MIN_TIMEOUT_MS = 60_000;
const MAX_TIMEOUT_MS = 300_000;
const MAX_SEEK_MS = 60_000;

// The longest intro animation on shortty ends at 3.6s; seeking past it keeps
// baselines out of the transient intro phase even when reduced-motion is off.
const DEFAULT_SEEK_MS = 3_600;

export function parseGoDuration(spec: string): number {
  const pattern = /^(?:(\d+)m)?(?:(\d+)s)?$/;
  const match = pattern.exec(spec.trim());
  if (!match || (match[1] === undefined && match[2] === undefined)) {
    throw new Error(`invalid duration ${JSON.stringify(spec)}`);
  }
  const minutes = match[1] ? Number(match[1]) : 0;
  const seconds = match[2] ? Number(match[2]) : 0;
  return minutes * 60_000 + seconds * 1_000;
}

const LOOPBACK_HOSTS = new Set(["127.0.0.1", "localhost", "[::1]"]);

export function loadCanaryConfig(env: Record<string, string | undefined>): CanaryConfig {
  const timeoutMs = parseGoDuration(env.VISUAL_TIMEOUT?.trim() || "2m");
  if (timeoutMs < MIN_TIMEOUT_MS || timeoutMs > MAX_TIMEOUT_MS) {
    throw new Error("VISUAL_TIMEOUT must be between 1m and 5m");
  }

  const targetUrl = env.VISUAL_TARGET_URL?.trim() ?? "";
  let parsed: URL;
  try {
    parsed = new URL(targetUrl);
  } catch {
    throw new Error("VISUAL_TARGET_URL must be an absolute URL");
  }
  const httpAllowed = LOOPBACK_HOSTS.has(parsed.hostname) || env.VISUAL_ALLOW_HTTP === "1";
  if (parsed.protocol !== "https:" && !(parsed.protocol === "http:" && httpAllowed)) {
    throw new Error("VISUAL_TARGET_URL must use HTTPS (or HTTP with VISUAL_ALLOW_HTTP=1)");
  }

  const targetName = env.VISUAL_TARGET?.trim() || "shortty";
  const target = TARGETS[targetName];
  if (!target) {
    throw new Error(
      `unknown target ${JSON.stringify(targetName)}; available: ${Object.keys(TARGETS).join(", ")}`,
    );
  }

  const seekMs = env.VISUAL_SEEK_MS?.trim() ? Number(env.VISUAL_SEEK_MS.trim()) : DEFAULT_SEEK_MS;
  if (!Number.isInteger(seekMs) || seekMs < 0 || seekMs > MAX_SEEK_MS) {
    throw new Error(`VISUAL_SEEK_MS must be an integer between 0 and ${MAX_SEEK_MS}`);
  }

  return {
    targetUrl,
    target,
    formFactors: parseFormFactors(env.VISUAL_FORM_FACTORS),
    seekMs,
    timeoutMs,
  };
}

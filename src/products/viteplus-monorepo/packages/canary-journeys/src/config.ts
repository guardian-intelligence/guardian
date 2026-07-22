export interface JourneyConfig {
  pageUrl: string;
  guardianHost: string;
  githubUsername: string;
  githubPassword: string;
  githubTotpSeed: string;
  timeoutMs: number;
}

const MIN_TIMEOUT_MS = 60_000;
const MAX_TIMEOUT_MS = 300_000;

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

export function loadJourneyConfig(env: Record<string, string | undefined>): JourneyConfig {
  const timeoutMs = parseGoDuration(env.CANARY_TIMEOUT?.trim() || "2m30s");
  if (timeoutMs < MIN_TIMEOUT_MS || timeoutMs > MAX_TIMEOUT_MS) {
    throw new Error("CANARY_TIMEOUT must be between 1m and 5m");
  }
  const pageUrl = env.POSTFLIGHT_URL?.trim() || "https://guardianintelligence.org/postflight";
  let parsed: URL;
  try {
    parsed = new URL(pageUrl);
  } catch {
    throw new Error("POSTFLIGHT_URL must be an absolute HTTPS URL");
  }
  if (parsed.protocol !== "https:" || parsed.hostname === "") {
    throw new Error("POSTFLIGHT_URL must be an absolute HTTPS URL");
  }
  const githubUsername = env.GITHUB_CANARY_USERNAME?.trim() ?? "";
  const githubPassword = env.GITHUB_CANARY_PASSWORD ?? "";
  const githubTotpSeed = env.GITHUB_CANARY_TOTP_SECRET?.trim() ?? "";
  if (githubUsername === "" || githubPassword === "" || githubTotpSeed === "") {
    throw new Error("GitHub canary username, password, and TOTP secret are required");
  }
  return {
    pageUrl,
    guardianHost: parsed.hostname,
    githubUsername,
    githubPassword,
    githubTotpSeed,
    timeoutMs,
  };
}

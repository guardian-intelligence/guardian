export type CompanyScenePhase = "beacon" | "hold" | "materialize" | "rules" | "settled" | "shimmer";

export type CompanySceneFrame = {
  readonly ambient: number;
  readonly beacon: number;
  readonly copy: number;
  readonly elapsedMs: number;
  readonly eyebrow: number;
  readonly ledeRules: number;
  readonly materialization: number;
  readonly nodes: number;
  readonly pencil: number;
  readonly phase: CompanyScenePhase;
  readonly rails: number;
  readonly shimmerProgress: number | null;
  readonly title: number;
};

export const COMPANY_SCENE_TIMING = {
  beaconEndMs: 3_200,
  beaconStartMs: 1_970,
  holdEndMs: 250,
  introEndMs: 4_300,
  ledeRulesEndMs: 4_300,
  ledeRulesStartMs: 3_600,
  materializationEndMs: 2_400,
  railsEndMs: 3_600,
  shimmerDurationMs: 4_800,
  shimmerMaximumDelayMs: 10_500,
  shimmerMinimumDelayMs: 5_500,
} as const;

const SHIMMER_DELAY_SEQUENCE_LENGTH = 32;

function clamp01(value: number) {
  return Math.min(1, Math.max(0, value));
}

function progressBetween(elapsedMs: number, startMs: number, endMs: number) {
  return clamp01((elapsedMs - startMs) / (endMs - startMs));
}

function smoothstep(value: number) {
  const progress = clamp01(value);
  return progress * progress * (3 - 2 * progress);
}

function shimmerDelayNoise(cycleIndex: number) {
  let value = Math.max(0, Math.floor(cycleIndex)) + 1;
  value = Math.imul(value ^ (value >>> 16), 0x21f0_aaad);
  value = Math.imul(value ^ (value >>> 15), 0x735a_2d97);
  return ((value ^ (value >>> 15)) >>> 0) / 0xffff_ffff;
}

export function companySceneShimmerDelayMs(cycleIndex: number) {
  const sequenceIndex = Math.max(0, Math.floor(cycleIndex)) % SHIMMER_DELAY_SEQUENCE_LENGTH;
  const delayRange =
    COMPANY_SCENE_TIMING.shimmerMaximumDelayMs - COMPANY_SCENE_TIMING.shimmerMinimumDelayMs;
  return Math.round(
    COMPANY_SCENE_TIMING.shimmerMinimumDelayMs + shimmerDelayNoise(sequenceIndex) * delayRange,
  );
}

const SHIMMER_DELAY_SEQUENCE = Array.from(
  { length: SHIMMER_DELAY_SEQUENCE_LENGTH },
  (_, cycleIndex) => companySceneShimmerDelayMs(cycleIndex),
);
const SHIMMER_SEQUENCE_DURATION_MS = SHIMMER_DELAY_SEQUENCE.reduce(
  (duration, delay) => duration + delay + COMPANY_SCENE_TIMING.shimmerDurationMs,
  0,
);

export type CompanySceneShimmerState = {
  readonly cycleIndex: number;
  readonly delayMs: number;
  readonly nextWakeDelayMs: number;
  readonly progress: number | null;
};

export function companySceneShimmerState(elapsedMs: number): CompanySceneShimmerState {
  const postIntroMs = Math.max(0, elapsedMs - COMPANY_SCENE_TIMING.introEndMs);
  const completedSequences = Math.floor(postIntroMs / SHIMMER_SEQUENCE_DURATION_MS);
  let remainingMs = postIntroMs - completedSequences * SHIMMER_SEQUENCE_DURATION_MS;
  let cycleIndex = completedSequences * SHIMMER_DELAY_SEQUENCE_LENGTH;

  for (const delayMs of SHIMMER_DELAY_SEQUENCE) {
    if (remainingMs < delayMs) {
      return {
        cycleIndex,
        delayMs,
        nextWakeDelayMs: delayMs - remainingMs,
        progress: null,
      };
    }

    remainingMs -= delayMs;
    if (remainingMs < COMPANY_SCENE_TIMING.shimmerDurationMs) {
      return {
        cycleIndex,
        delayMs,
        nextWakeDelayMs: 0,
        progress: clamp01(remainingMs / COMPANY_SCENE_TIMING.shimmerDurationMs),
      };
    }

    remainingMs -= COMPANY_SCENE_TIMING.shimmerDurationMs;
    cycleIndex += 1;
  }

  return {
    cycleIndex,
    delayMs: companySceneShimmerDelayMs(cycleIndex),
    nextWakeDelayMs: companySceneShimmerDelayMs(cycleIndex),
    progress: null,
  };
}

export function sampleCompanyScene(elapsedMs: number): CompanySceneFrame {
  const elapsed = Math.max(0, elapsedMs);
  const materializationTime = progressBetween(
    elapsed,
    COMPANY_SCENE_TIMING.holdEndMs,
    COMPANY_SCENE_TIMING.materializationEndMs,
  );
  const materialization = Math.pow(materializationTime, 1.45);
  const beacon = smoothstep(
    progressBetween(elapsed, COMPANY_SCENE_TIMING.beaconStartMs, COMPANY_SCENE_TIMING.beaconEndMs),
  );
  const ambientMaterialization = Math.pow(
    progressBetween(elapsed, COMPANY_SCENE_TIMING.holdEndMs, COMPANY_SCENE_TIMING.beaconStartMs),
    1.6,
  );
  const materialAmbient = smoothstep(ambientMaterialization) * 0.5;
  const ambient = materialAmbient + (1 - materialAmbient) * beacon;
  const rails = smoothstep(progressBetween(elapsed, 2_170, COMPANY_SCENE_TIMING.railsEndMs));
  const ledeRules = smoothstep(
    progressBetween(
      elapsed,
      COMPANY_SCENE_TIMING.ledeRulesStartMs,
      COMPANY_SCENE_TIMING.ledeRulesEndMs,
    ),
  );
  const pencil =
    smoothstep(progressBetween(elapsed, 2_090, 2_280)) *
    (1 - smoothstep(progressBetween(elapsed, 3_130, COMPANY_SCENE_TIMING.railsEndMs)));
  const shimmerProgress =
    elapsed >= COMPANY_SCENE_TIMING.introEndMs ? companySceneShimmerState(elapsed).progress : null;
  const phase: CompanyScenePhase =
    shimmerProgress !== null
      ? "shimmer"
      : elapsed >= COMPANY_SCENE_TIMING.introEndMs
        ? "settled"
        : elapsed >= COMPANY_SCENE_TIMING.ledeRulesStartMs
          ? "rules"
          : elapsed >= COMPANY_SCENE_TIMING.beaconStartMs
            ? "beacon"
            : elapsed >= COMPANY_SCENE_TIMING.holdEndMs
              ? "materialize"
              : "hold";

  return {
    ambient,
    beacon,
    copy: smoothstep(progressBetween(elapsed, 2_300, 3_500)),
    elapsedMs: elapsed,
    eyebrow: smoothstep(progressBetween(elapsed, 2_230, 3_400)),
    ledeRules,
    materialization,
    nodes: rails * 0.7,
    pencil,
    phase,
    rails,
    shimmerProgress,
    title: smoothstep(progressBetween(materialization, 0.38, 0.98)),
  };
}

export function companySceneNextWakeDelayMs(elapsedMs: number) {
  const elapsed = Math.max(0, elapsedMs);
  if (elapsed < COMPANY_SCENE_TIMING.introEndMs) return 0;
  return companySceneShimmerState(elapsed).nextWakeDelayMs;
}

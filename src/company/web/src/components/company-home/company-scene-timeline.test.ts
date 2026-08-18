import { describe, expect, it } from "vitest";
import {
  COMPANY_SCENE_TIMING,
  companySceneNextWakeDelayMs,
  companySceneShimmerDelayMs,
  companySceneShimmerState,
  sampleCompanyScene,
} from "./company-scene-timeline";

describe("company scene timeline", () => {
  it("holds the initial scene in darkness", () => {
    const frame = sampleCompanyScene(COMPANY_SCENE_TIMING.holdEndMs - 1);
    expect(frame.phase).toBe("hold");
    expect(frame.ambient).toBe(0);
    expect(frame.beacon).toBe(0);
    expect(frame.materialization).toBe(0);
  });

  it("raises ambient light during materialization before the beacon appears", () => {
    const frame = sampleCompanyScene(COMPANY_SCENE_TIMING.beaconStartMs - 1);
    expect(frame.phase).toBe("materialize");
    expect(frame.materialization).toBeGreaterThan(0.6);
    expect(frame.ambient).toBeGreaterThan(0.49);
    expect(frame.ambient).toBeLessThanOrEqual(0.5);
    expect(frame.beacon).toBe(0);
  });

  it("reaches full illumination through the beacon channel", () => {
    const frame = sampleCompanyScene(COMPANY_SCENE_TIMING.beaconEndMs);
    expect(frame.ambient).toBe(1);
    expect(frame.beacon).toBe(1);
  });

  it("draws the lede rules only after every existing intro channel completes", () => {
    const beforeRules = sampleCompanyScene(COMPANY_SCENE_TIMING.ledeRulesStartMs - 1);
    expect(beforeRules.ledeRules).toBe(0);
    expect(beforeRules.copy).toBe(1);
    expect(beforeRules.eyebrow).toBe(1);
    expect(beforeRules.rails).toBeGreaterThan(0.999);

    const drawing = sampleCompanyScene(
      (COMPANY_SCENE_TIMING.ledeRulesStartMs + COMPANY_SCENE_TIMING.ledeRulesEndMs) / 2,
    );
    expect(drawing.phase).toBe("rules");
    expect(drawing.ledeRules).toBeGreaterThan(0);
    expect(drawing.ledeRules).toBeLessThan(1);

    const settled = sampleCompanyScene(COMPANY_SCENE_TIMING.ledeRulesEndMs);
    expect(settled.phase).toBe("settled");
    expect(settled.ledeRules).toBe(1);
  });

  it("rests for a deterministic variable delay between shimmers", () => {
    const firstDelay = companySceneShimmerDelayMs(0);
    const secondDelay = companySceneShimmerDelayMs(1);
    const shimmerStart = COMPANY_SCENE_TIMING.introEndMs + firstDelay;
    expect(sampleCompanyScene(shimmerStart - 1).shimmerProgress).toBeNull();
    expect(sampleCompanyScene(shimmerStart + 1).shimmerProgress).toBeGreaterThan(0);
    expect(
      sampleCompanyScene(shimmerStart + COMPANY_SCENE_TIMING.shimmerDurationMs + 1).shimmerProgress,
    ).toBeNull();
    expect(companySceneNextWakeDelayMs(COMPANY_SCENE_TIMING.introEndMs)).toBe(firstDelay);
    expect(
      companySceneNextWakeDelayMs(shimmerStart + COMPANY_SCENE_TIMING.shimmerDurationMs + 1),
    ).toBe(secondDelay - 1);
    expect(companySceneShimmerState(shimmerStart + 1).cycleIndex).toBe(0);
    expect(companySceneShimmerState(shimmerStart + 1).nextWakeDelayMs).toBe(0);
  });

  it("keeps randomized waits bounded and nonuniform", () => {
    const delays = Array.from({ length: 32 }, (_, cycle) => companySceneShimmerDelayMs(cycle));
    expect(new Set(delays).size).toBeGreaterThan(24);
    expect(Math.min(...delays)).toBeGreaterThanOrEqual(COMPANY_SCENE_TIMING.shimmerMinimumDelayMs);
    expect(Math.max(...delays)).toBeLessThanOrEqual(COMPANY_SCENE_TIMING.shimmerMaximumDelayMs);
    expect(companySceneShimmerDelayMs(32)).toBe(companySceneShimmerDelayMs(0));
  });

  it("keeps every scalar channel normalized", () => {
    for (let elapsedMs = 0; elapsedMs <= 20_000; elapsedMs += 79) {
      const {
        phase: _phase,
        shimmerProgress,
        elapsedMs: _elapsedMs,
        ...channels
      } = sampleCompanyScene(elapsedMs);
      for (const value of Object.values(channels)) {
        expect(value).toBeGreaterThanOrEqual(0);
        expect(value).toBeLessThanOrEqual(1);
      }
      if (shimmerProgress !== null) {
        expect(shimmerProgress).toBeGreaterThanOrEqual(0);
        expect(shimmerProgress).toBeLessThanOrEqual(1);
      }
    }
  });
});

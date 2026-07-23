import type { Page } from "@playwright/test";
import { classifyFold, measureFold, type FoldResult } from "./fold-probe.ts";

export async function checkFold(
  page: Page,
  selectors: readonly string[],
  tolerancePx = 0,
): Promise<FoldResult[]> {
  const measurements = await page.evaluate(measureFold, [...selectors]);
  return measurements.map((m) => classifyFold(m, tolerancePx));
}

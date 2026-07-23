export type FoldStatus = "above" | "partial" | "below" | "missing";

export interface FoldMeasurement {
  selector: string;
  found: boolean;
  top: number;
  bottom: number;
  viewportHeight: number;
}

export interface FoldResult {
  selector: string;
  status: FoldStatus;
  clippedPx: number;
}

export function classifyFold(measurement: FoldMeasurement, tolerancePx = 0): FoldResult {
  const { selector, found, top, bottom, viewportHeight } = measurement;
  if (!found) return { selector, status: "missing", clippedPx: 0 };
  if (top >= viewportHeight - tolerancePx) {
    return { selector, status: "below", clippedPx: Math.round(bottom - top) };
  }
  if (bottom > viewportHeight + tolerancePx) {
    return { selector, status: "partial", clippedPx: Math.round(bottom - viewportHeight) };
  }
  return { selector, status: "above", clippedPx: 0 };
}

export function foldFailures(results: readonly FoldResult[]): FoldResult[] {
  return results.filter((r) => r.status !== "above");
}

/** Serialized into the page by fold.ts; must stay self-contained. */
export function measureFold(selectors: string[]): FoldMeasurement[] {
  return selectors.map((selector) => {
    const element = document.querySelector(selector);
    if (!element) {
      return { selector, found: false, top: 0, bottom: 0, viewportHeight: window.innerHeight };
    }
    const rect = element.getBoundingClientRect();
    return {
      selector,
      found: true,
      top: rect.top,
      bottom: rect.bottom,
      viewportHeight: window.innerHeight,
    };
  });
}

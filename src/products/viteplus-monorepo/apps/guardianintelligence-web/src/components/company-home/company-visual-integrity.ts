import { emitSpan } from "~/lib/telemetry/browser";

export interface CompanyVisualIntegrityMetrics {
  readonly layoutShift: number;
  readonly longFrameCount: number;
  readonly longFrameTotalMs: number;
  readonly maxLongFrameMs: number;
  readonly motionExpected: boolean;
  readonly prematureContent: boolean;
}

interface LayoutShiftEntry extends PerformanceEntry {
  readonly hadRecentInput: boolean;
  readonly value: number;
}

export function companyVisualIntegrityFailures(metrics: CompanyVisualIntegrityMetrics) {
  const failures: string[] = [];
  if (metrics.motionExpected && metrics.prematureContent) failures.push("premature-content");
  if (metrics.layoutShift > 0.02) failures.push("layout-shift");
  if (
    metrics.motionExpected &&
    (metrics.maxLongFrameMs >= 250 ||
      (metrics.longFrameCount >= 3 && metrics.longFrameTotalMs >= 300))
  ) {
    failures.push("frame-thrash");
  }
  return failures;
}

function renderedOpacity(element: Element | null) {
  if (!element) return 0;
  let opacity = 1;
  let current: Element | null = element;
  while (current) {
    const style = getComputedStyle(current);
    if (style.display === "none" || style.visibility !== "visible") return 0;
    opacity *= Number.parseFloat(style.opacity) || 0;
    current = current.parentElement;
  }
  return opacity;
}

export function monitorCompanyVisualIntegrity() {
  const state = {
    disposed: false,
    finishRequested: false,
    finished: false,
    firstFrameOpacity: 0,
    firstFrameExperience: "unknown",
    firstFrameSampled: false,
    frameObserver: "none",
    layoutShift: 0,
    layoutShiftCount: 0,
    longFrameCount: 0,
    longFrameTotalMs: 0,
    maxLongFrameMs: 0,
  };
  const observers: PerformanceObserver[] = [];
  const supported =
    typeof PerformanceObserver === "undefined"
      ? []
      : (PerformanceObserver.supportedEntryTypes ?? []);

  const observe = (
    type: string,
    callback: PerformanceObserverCallback,
  ): PerformanceObserver | null => {
    try {
      const observer = new PerformanceObserver(callback);
      observer.observe({ buffered: true, type });
      observers.push(observer);
      return observer;
    } catch {
      return null;
    }
  };

  if (supported.includes("layout-shift")) {
    observe("layout-shift", (list) => {
      for (const entry of list.getEntries() as LayoutShiftEntry[]) {
        if (entry.hadRecentInput) continue;
        state.layoutShift += entry.value;
        state.layoutShiftCount += 1;
      }
    });
  }

  const frameEntryType = supported.includes("long-animation-frame")
    ? "long-animation-frame"
    : supported.includes("longtask")
      ? "longtask"
      : null;
  if (frameEntryType) {
    const observer = observe(frameEntryType, (list) => {
      for (const entry of list.getEntries()) {
        state.longFrameCount += 1;
        state.longFrameTotalMs += entry.duration;
        state.maxLongFrameMs = Math.max(state.maxLongFrameMs, entry.duration);
      }
    });
    if (observer) state.frameObserver = frameEntryType;
  }

  const finish = () => {
    state.finishRequested = true;
    if (
      state.disposed ||
      state.finished ||
      !state.firstFrameSampled ||
      window.location.pathname !== "/"
    ) {
      return;
    }
    state.finished = true;
    for (const observer of observers) observer.disconnect();
    const metrics: CompanyVisualIntegrityMetrics = {
      layoutShift: state.layoutShift,
      longFrameCount: state.longFrameCount,
      longFrameTotalMs: state.longFrameTotalMs,
      maxLongFrameMs: state.maxLongFrameMs,
      motionExpected: state.firstFrameExperience === "pending",
      prematureContent: state.firstFrameOpacity > 0.02,
    };
    const failures = companyVisualIntegrityFailures(metrics);
    const root = document.documentElement;
    const canvas = document.querySelector<HTMLElement>(".illumination-canvas");
    const attrs = {
      "route.path": window.location.pathname,
      "visual.canvas_mode": root.dataset.canvasMode ?? "unavailable",
      "visual.experience": root.dataset.companyExperience ?? "unknown",
      "visual.failure": failures.join(","),
      "visual.first_frame_experience": state.firstFrameExperience,
      "visual.first_frame_opacity": state.firstFrameOpacity.toFixed(4),
      "visual.frame_observer": state.frameObserver,
      "visual.layout_shift": state.layoutShift.toFixed(4),
      "visual.layout_shift_count": String(state.layoutShiftCount),
      "visual.long_frame_count": String(state.longFrameCount),
      "visual.long_frame_total_ms": state.longFrameTotalMs.toFixed(1),
      "visual.max_long_frame_ms": state.maxLongFrameMs.toFixed(1),
      "visual.monitor_version": "1",
    };
    root.dataset.companyVisualIntegrity = failures.length === 0 ? "ok" : "failed";
    if (canvas) canvas.dataset.visualIntegrity = root.dataset.companyVisualIntegrity;
    emitSpan("company.hero_visual_integrity_checked", attrs);
    if (failures.length > 0) emitSpan("company.hero_visual_integrity_failed", attrs);
  };

  const firstFrame = window.requestAnimationFrame(() => {
    const elements = [
      document.querySelector(".company-home-title__outline"),
      document.querySelector(".company-home-hero__eyebrow"),
      document.querySelector(".company-home-hero__lede"),
      document.querySelector(".company-home-beacon"),
    ];
    state.firstFrameOpacity = Math.max(...elements.map(renderedOpacity));
    state.firstFrameExperience = document.documentElement.dataset.companyExperience ?? "unknown";
    state.firstFrameSampled = true;
    if (state.finishRequested) finish();
  });

  return {
    finish,
    dispose() {
      state.disposed = true;
      window.cancelAnimationFrame(firstFrame);
      for (const observer of observers) observer.disconnect();
    },
  };
}

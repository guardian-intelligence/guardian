"use client";

import { useEffect, useRef } from "react";
import { useRouterState } from "@tanstack/react-router";
import { onCLS, onINP, onLCP, type Metric } from "web-vitals";
import { emitSpan } from "./browser";
import { installErrorCapture } from "./errors";
import { installFetchTelemetry } from "./fetch-telemetry";

let webVitalsInstalled = false;

function installWebVitals(): void {
  if (webVitalsInstalled || typeof window === "undefined") return;
  webVitalsInstalled = true;

  const handler = (kind: "lcp" | "cls" | "inp") => (metric: Metric) => {
    emitSpan(`web_vital.${kind}`, {
      "web_vital.id": metric.id,
      "web_vital.name": metric.name,
      "web_vital.rating": metric.rating,
      "web_vital.value": String(metric.value),
      "web_vital.delta": String(metric.delta),
      "web_vital.navigation_type": metric.navigationType,
      "route.path": window.location.pathname,
    });
  };

  onLCP(handler("lcp"));
  onCLS(handler("cls"));
  onINP(handler("inp"));
}

// Mounted at the root. Emits `<app>.route_view` on initial load and every
// subsequent SPA route resolution, installs the global error capture, and
// registers Web Vitals — one mount wires the whole telemetry surface.
export function TelemetryProbe({ app }: { app: string }) {
  const path = useRouterState({ select: (state) => state.location.pathname });
  const previousPath = useRef<string | undefined>(undefined);

  useEffect(() => {
    installErrorCapture(app);
    installFetchTelemetry(app);
    installWebVitals();
    // The beacon loads strictly off the critical path: dynamic import on
    // idle, so it is a lazy chunk (never modulepreloaded) and its cost is
    // paid after LCP/TTI. Events emitted before it loads wait in the
    // bounded queue.
    const idle: (cb: () => void) => void =
      typeof window.requestIdleCallback === "function"
        ? (cb) => window.requestIdleCallback(cb, { timeout: 5000 })
        : (cb) => void setTimeout(cb, 2000);
    idle(() => {
      void import("./beacon").then((m) => m.initBeacon(app));
    });
  }, [app]);

  useEffect(() => {
    if (typeof window === "undefined") return;
    const navigation: PerformanceNavigationTiming | undefined = performance.getEntriesByType(
      "navigation",
    )[0] as PerformanceNavigationTiming | undefined;
    emitSpan(`${app}.route_view`, {
      "route.path": path,
      "route.host": window.location.host,
      "route.previous_path": previousPath.current ?? "",
      referrer: document.referrer,
      "navigation.type": navigation?.type ?? "unknown",
    });
    previousPath.current = path;
  }, [app, path]);

  return null;
}

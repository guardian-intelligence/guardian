import { getRouteApi } from "@tanstack/react-router";
import { Suspense, lazy, useEffect } from "react";
import { emitSpan } from "~/lib/telemetry/browser";
import { TamControls } from "./tam-controls";

// The canvas chart pulls in d3 + pretext and only runs client-side. Lazy-load
// it (the FirstLight pattern) so those libraries land in a hydration-time chunk
// instead of the SSR bundle and the initial client payload. The endpoint KPIs
// render server-side, so the headline numbers are present before hydration.
const TamChart = lazy(() => import("./tam-chart").then((m) => ({ default: m.TamChart })));

function ChartFallback() {
  return <div aria-hidden style={{ width: "100%", aspectRatio: "16 / 9", minHeight: "260px" }} />;
}
import {
  TAM_LEVERS,
  clampLever,
  endpointOf,
  formatTamBillion,
  leverValue,
  project,
  resolveInput,
  toCSV,
  type TamLeverSpec,
  type TamProjectionDefaults,
  type TamProjectionPoint,
} from "./model";

// Composition root for the bare-metal TAM scenario playground. It owns the
// glue: read the validated URL search, resolve the projection input, recompute
// the points, and fan the data out to the KPIs, controls, and chart.
// URL search params are the single state authority — there is no separate store.

const routeApi = getRouteApi("/news/$slug");

export interface BareMetalTamPlaygroundProps {
  readonly slug: string;
  readonly defaults: TamProjectionDefaults;
}

export function BareMetalTamPlayground({ slug, defaults }: BareMetalTamPlaygroundProps) {
  const search = routeApi.useSearch();
  const navigate = routeApi.useNavigate();

  const input = resolveInput(defaults, search);
  const points = project(input);
  const endpoint = endpointOf(points);
  const isModified = TAM_LEVERS.some((spec) => leverValue(input, spec) !== defaults[spec.key]);

  const telemetryAttrs = (): Record<string, string> => ({
    "article.slug": slug,
    cloud_now_billion: String(input.currentCloudTamBillion),
    cloud_2030_billion: String(input.cloudTam2030Billion),
    segment_cagr_pct: String(input.segmentCagrPct),
    endpoint_standard_projection_billion: String(Math.round(endpoint.standardProjectionTamBillion)),
    endpoint_guardian_projection_billion: String(Math.round(endpoint.guardianProjectionTamBillion)),
    endpoint_enthusiast_demand_billion: String(Math.round(endpoint.enthusiastDemandBillion)),
  });

  // One view span per article load.
  useEffect(() => {
    if (typeof window === "undefined") return;
    emitSpan("company.tam_playground.view", telemetryAttrs());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [slug]);

  // Live write: clamp + drop params equal to the default so shared URLs stay
  // short and only carry what the reader actually moved.
  const setLever = (spec: TamLeverSpec, value: number) => {
    const clamped = clampLever(spec, value);
    void navigate({
      replace: true,
      // Keep the reader's scroll position — every slider tick writes the URL,
      // and the router's default scroll-to-top would yank the page up mid-drag.
      resetScroll: false,
      search: (prev: Record<string, unknown>) => {
        const next = { ...prev };
        if (clamped === undefined || clamped === defaults[spec.key]) {
          delete next[spec.param];
        } else {
          next[spec.param] = clamped;
        }
        return next;
      },
    });
  };

  const settleLever = (spec: TamLeverSpec, value: number) => {
    const clamped = clampLever(spec, value);
    if (clamped === undefined) return;
    emitSpan("company.tam_playground.lever_change", {
      ...telemetryAttrs(),
      lever: spec.param,
      lever_value: String(clamped),
    });
  };

  const reset = () => {
    void navigate({ replace: true, resetScroll: false, search: {} });
  };

  const downloadCsv = () => {
    if (typeof document === "undefined") return;
    const csv = toCSV(points);
    const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `tam-playground-segment-cagr-${input.segmentCagrPct}.csv`;
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    URL.revokeObjectURL(url);
    emitSpan("company.tam_playground.csv_download", telemetryAttrs());
  };

  return (
    <section
      data-tam-playground
      className="my-4 flex flex-col gap-7 rounded-sm p-5 md:p-7"
      style={{
        border: "1px solid var(--treatment-hairline)",
        background: "var(--treatment-ground)",
      }}
    >
      <KpiRow endpoint={endpoint} />

      <div className="grid grid-cols-1 gap-7 lg:grid-cols-5">
        <div className="lg:col-span-3">
          <Suspense fallback={<ChartFallback />}>
            <TamChart points={points} />
          </Suspense>
        </div>
        <div className="lg:col-span-2">
          <TamControls
            input={input}
            onLeverChange={setLever}
            onLeverSettle={settleLever}
            onReset={reset}
            onDownloadCsv={downloadCsv}
            isModified={isModified}
          />
        </div>
      </div>
    </section>
  );
}

// The two headline numbers: external standard projection and Guardian's
// projection with enthusiast demand stacked in.
function KpiRow({ endpoint }: { endpoint: TamProjectionPoint }) {
  const kpis = [
    {
      label: "Standard Projection · 2030",
      value: endpoint.standardProjectionTamBillion,
      accent: false,
    },
    {
      label: "Guardian Projection · 2030",
      value: endpoint.guardianProjectionTamBillion,
      accent: true,
    },
  ];
  return (
    <div
      className="grid grid-cols-1 gap-px sm:grid-cols-2"
      style={{ background: "var(--treatment-hairline)" }}
      data-tam-kpis
    >
      {kpis.map((kpi) => (
        <div
          key={kpi.label}
          className="flex flex-col gap-1 px-4 py-3"
          style={{ background: "var(--treatment-ground)" }}
        >
          <span
            className="font-mono text-[10px] font-semibold uppercase"
            style={{ color: "var(--treatment-muted-meta)", letterSpacing: "0.12em" }}
          >
            {kpi.label}
          </span>
          <span
            style={{
              fontFamily: "'Fraunces', Georgia, serif",
              fontVariationSettings: '"opsz" 80',
              fontSize: kpi.accent ? "34px" : "28px",
              lineHeight: 1.05,
              color: "var(--treatment-ink)",
            }}
          >
            {formatTamBillion(kpi.value)}
          </span>
          {kpi.accent ? (
            <span
              aria-hidden
              style={{ height: "3px", width: "40px", background: "var(--color-flare)" }}
            />
          ) : null}
        </div>
      ))}
    </div>
  );
}

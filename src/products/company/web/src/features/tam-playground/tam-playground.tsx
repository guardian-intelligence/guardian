import { getRouteApi } from "@tanstack/react-router";
import { Suspense, lazy, useEffect } from "react";
import { emitSpan } from "~/lib/telemetry/browser";
import { TamControls } from "./tam-controls";
import { TamSummaryTable } from "./tam-summary-table";

// The canvas chart pulls in d3 + pretext and only runs client-side. Lazy-load
// it (the FirstLight pattern) so those libraries land in a hydration-time chunk
// instead of the SSR bundle and the initial client payload. The summary table
// below is the pre-hydration reading path, so deferring the chart costs nothing.
const TamChart = lazy(() => import("./tam-chart").then((m) => ({ default: m.TamChart })));

function ChartFallback() {
  return <div aria-hidden style={{ width: "100%", aspectRatio: "16 / 9", minHeight: "260px" }} />;
}
import {
  TAM_LEVERS,
  TAM_SERIES,
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
// the points, and fan the data out to the KPIs, controls, chart, and table.
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
    hobbyist_growth_pct: String(input.hobbyistGrowthPct),
    software_factory_growth_pct: String(input.softwareFactoryGrowthPct),
    endpoint_cloud_indexed_billion: String(Math.round(endpoint.cloudIndexedBareMetalTamBillion)),
    endpoint_pc_builder_billion: String(Math.round(endpoint.pcBuilderTamBillion)),
    endpoint_default_company_billion: String(
      Math.round(endpoint.defaultSoftwareCompanyTamBillion),
    ),
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
    void navigate({ replace: true, search: {} });
  };

  const downloadCsv = () => {
    if (typeof document === "undefined") return;
    const csv = toCSV(points);
    const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `bare-metal-tam-${input.currentCloudTamBillion}-${input.cloudTam2030Billion}-${input.hobbyistGrowthPct}-${input.softwareFactoryGrowthPct}.csv`;
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
      <header className="flex flex-col gap-2">
        <p
          className="font-mono text-[10px] font-semibold uppercase"
          style={{ color: "var(--treatment-muted-meta)", letterSpacing: "0.2em", margin: 0 }}
        >
          Scenario playground
        </p>
        <p
          style={{
            fontFamily: "'Geist', sans-serif",
            fontSize: "14px",
            lineHeight: 1.5,
            color: "var(--treatment-muted-strong)",
            margin: 0,
            maxWidth: "64ch",
          }}
        >
          Move the four levers and the projection recomputes live. Every value lives in the
          URL, so a scenario you like is a link you can share. This is a model, not a forecast.
        </p>
      </header>

      <KpiRow endpoint={endpoint} />

      <div className="grid grid-cols-1 gap-7 lg:grid-cols-5">
        <div className="flex flex-col gap-4 lg:col-span-3">
          <Suspense fallback={<ChartFallback />}>
            <TamChart points={points} />
          </Suspense>
          <Assumptions
            currentBareMetalTamBillion={input.currentBareMetalTamBillion}
            hobbyistUpliftBillion={endpoint.pcBuilderTamBillion - endpoint.cloudIndexedBareMetalTamBillion}
            hobbyistGrowthPct={input.hobbyistGrowthPct}
            softwareFactoryGrowthPct={input.softwareFactoryGrowthPct}
          />
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

      <TamSummaryTable points={points} />
    </section>
  );
}

function KpiRow({ endpoint }: { endpoint: TamProjectionPoint }) {
  return (
    <div
      className="grid grid-cols-1 gap-px sm:grid-cols-3"
      style={{ background: "var(--treatment-hairline)" }}
      data-tam-kpis
    >
      {TAM_SERIES.map((series) => {
        const thesis = series.emphasis === "thesis";
        return (
          <div
            key={series.key}
            className="flex flex-col gap-1 px-4 py-3"
            style={{ background: "var(--treatment-ground)" }}
          >
            <span
              className="font-mono text-[10px] font-semibold uppercase"
              style={{ color: "var(--treatment-muted-meta)", letterSpacing: "0.12em" }}
            >
              {series.short} · 2030
            </span>
            <span
              style={{
                fontFamily: "'Fraunces', Georgia, serif",
                fontVariationSettings: '"opsz" 80',
                fontSize: thesis ? "34px" : "28px",
                lineHeight: 1.05,
                color: "var(--treatment-ink)",
              }}
            >
              {formatTamBillion(endpoint[series.key])}
            </span>
            {thesis ? (
              <span
                aria-hidden
                style={{ height: "3px", width: "40px", background: "var(--color-flare)" }}
              />
            ) : null}
          </div>
        );
      })}
    </div>
  );
}

function Assumptions({
  currentBareMetalTamBillion,
  hobbyistUpliftBillion,
  hobbyistGrowthPct,
  softwareFactoryGrowthPct,
}: {
  currentBareMetalTamBillion: number;
  hobbyistUpliftBillion: number;
  hobbyistGrowthPct: number;
  softwareFactoryGrowthPct: number;
}) {
  const rows: ReadonlyArray<readonly [string, string]> = [
    ["Bare-metal baseline", formatTamBillion(currentBareMetalTamBillion)],
    [
      "Hobbyist displacement",
      `${hobbyistGrowthPct}% of cloud-indexed · +${formatTamBillion(hobbyistUpliftBillion)}`,
    ],
    [
      "Software factory",
      `cloud-indexed +${softwareFactoryGrowthPct}% · ${(1 + softwareFactoryGrowthPct / 100).toFixed(1)}x`,
    ],
    ["Window", "2026Q3 → 2030Q4"],
  ];
  return (
    <dl className="flex flex-col gap-1.5" data-tam-assumptions>
      <p
        className="font-mono text-[10px] font-semibold uppercase"
        style={{ color: "var(--treatment-muted-meta)", letterSpacing: "0.2em", margin: "0 0 2px" }}
      >
        Fixed assumptions
      </p>
      {rows.map(([term, value]) => (
        <div key={term} className="flex items-baseline justify-between gap-4">
          <dt
            style={{
              fontFamily: "'Geist', sans-serif",
              fontSize: "12px",
              color: "var(--treatment-muted)",
            }}
          >
            {term}
          </dt>
          <dd
            className="font-mono"
            style={{
              fontSize: "12px",
              color: "var(--treatment-muted-strong)",
              fontVariantNumeric: "tabular-nums",
              margin: 0,
              textAlign: "right",
            }}
          >
            {value}
          </dd>
        </div>
      ))}
    </dl>
  );
}

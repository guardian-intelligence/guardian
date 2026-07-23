import { context, trace, type Context, type Span, type Tracer } from "@opentelemetry/api";

// Server-side tracing for the app's one perimeter-crossing hop
// (/api/resolve → X's syndication endpoint). Same contract as the Go
// producers: OTEL_EXPORTER_OTLP_TRACES_ENDPOINT unset means a no-op tracer —
// observability never gates the service. The SDK loads behind
// import.meta.env.SSR so none of it can reach a client bundle, and spans are
// parented explicitly (childContext) rather than via an async context
// manager, which sdk-trace-base deliberately does not ship.

async function start(): Promise<void> {
  const endpoint = process.env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT ?? "";
  if (endpoint === "") return;
  const [
    { BasicTracerProvider, BatchSpanProcessor },
    { OTLPTraceExporter },
    { resourceFromAttributes },
  ] = await Promise.all([
    import("@opentelemetry/sdk-trace-base"),
    import("@opentelemetry/exporter-trace-otlp-http"),
    import("@opentelemetry/resources"),
  ]);
  const image = process.env.GUARDIAN_IMAGE ?? "";
  const provider = new BasicTracerProvider({
    resource: resourceFromAttributes({
      "service.name": "shortty-web",
      "service.version": image.split("@")[1] ?? image,
      "guardian.image": image,
      "guardian.site": process.env.GUARDIAN_SITE ?? "",
      "guardian.deploy_id": process.env.GUARDIAN_DEPLOY_ID ?? "",
    }),
    spanProcessors: [new BatchSpanProcessor(new OTLPTraceExporter({ url: endpoint }))],
  });
  trace.setGlobalTracerProvider(provider);
  process.once("SIGTERM", () => {
    void provider.shutdown();
  });
}

// Handlers await this before their first span so no request races the
// provider registration into the no-op tracer.
export const ready: Promise<void> = import.meta.env.SSR ? start() : Promise.resolve();

export function tracer(): Tracer {
  return trace.getTracer("guardian/shortty-web");
}

export function childContext(parent: Span): Context {
  return trace.setSpan(context.active(), parent);
}

// The no-op tracer hands out the invalid (all-zero) trace id; only a real
// one is worth surfacing to clients.
export function exportedTraceId(span: Span): string {
  const id = span.spanContext().traceId;
  return /^0+$/.test(id) ? "" : id;
}

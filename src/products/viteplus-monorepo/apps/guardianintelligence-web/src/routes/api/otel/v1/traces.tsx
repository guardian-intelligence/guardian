import { createFileRoute } from "@tanstack/react-router";

// Same-origin OTLP HTTP forwarder. The browser cannot reach the otel collector
// directly: CSP `connect-src 'self'` blocks cross-origin POSTs, so this server
// route accepts OTLP-JSON POSTs and forwards them to an explicitly configured
// in-cluster collector endpoint.
//
// Body cap: 256 KiB. The web SDK's BatchSpanProcessor batches up to 50 spans
// before exporting; even with attribute-heavy spans we expect single-digit KB
// per request. Anything larger is suspicious and rejected loudly.

const MAX_BODY_BYTES = 256 * 1024;

function configuredEnv(name: string): string | undefined {
  const value = process.env[name]?.trim();
  return value === "" ? undefined : value;
}

function collectorTracesURL(): string | undefined {
  const tracesEndpoint = configuredEnv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT");
  if (tracesEndpoint !== undefined) return tracesEndpoint;

  const endpoint = configuredEnv("OTEL_EXPORTER_OTLP_ENDPOINT");
  if (endpoint === undefined) return undefined;
  return `${endpoint.replace(/\/+$/, "")}/v1/traces`;
}

export const Route = createFileRoute("/api/otel/v1/traces")({
  server: {
    handlers: {
      POST: async ({ request }) => {
        const contentType = request.headers.get("content-type") ?? "";
        if (!contentType.startsWith("application/json")) {
          return new Response("expected application/json", {
            status: 415,
            headers: { "content-type": "text/plain" },
          });
        }

        const body = await request.arrayBuffer();
        if (body.byteLength === 0) {
          return new Response("empty body", {
            status: 400,
            headers: { "content-type": "text/plain" },
          });
        }
        if (body.byteLength > MAX_BODY_BYTES) {
          return new Response("payload too large", {
            status: 413,
            headers: { "content-type": "text/plain" },
          });
        }

        const collectorURL = collectorTracesURL();
        if (collectorURL === undefined) {
          return new Response("otel exporter endpoint not configured", {
            status: 503,
            headers: { "content-type": "text/plain" },
          });
        }

        try {
          const upstream = await fetch(collectorURL, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body,
          });
          return new Response(upstream.body, {
            status: upstream.status,
            headers: {
              "content-type": upstream.headers.get("content-type") ?? "application/json",
            },
          });
        } catch (error) {
          const message = error instanceof Error ? error.message : String(error);
          return new Response(`otelcol unreachable: ${message}`, {
            status: 502,
            headers: { "content-type": "text/plain" },
          });
        }
      },
    },
  },
});

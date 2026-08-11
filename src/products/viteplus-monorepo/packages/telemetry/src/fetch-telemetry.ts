// By-construction rpc capture: wraps window.fetch so every same-origin
// request carries a W3C traceparent minted client-side and lands in the
// events pipeline as `<app>.rpc` under that trace id. Server middlewares
// continue the same id into their spans, so one id quoted from either side
// follows the whole call. Cross-origin requests are left untouched (a
// traceparent header would force CORS preflights on third parties), and
// the ingest endpoint itself is excluded — instrumenting the beacon's own
// publish would feed the queue it drains.

import { emitSpan } from "./browser";
import { newTraceIdHex } from "./trace-id";
import { reportError } from "./errors";

const INGEST_PREFIX = "/api/events/";

let installed = false;

function spanIdHex(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(8));
  let hex = "";
  for (const b of bytes) hex += b.toString(16).padStart(2, "0");
  return hex;
}

function sameOriginPath(input: RequestInfo | URL): string | null {
  try {
    const url = new URL(input instanceof Request ? input.url : String(input), window.location.href);
    if (url.origin !== window.location.origin) return null;
    return url.pathname;
  } catch {
    return null;
  }
}

export function installFetchTelemetry(app: string): void {
  if (installed || typeof window === "undefined") return;
  installed = true;
  const original = window.fetch.bind(window);

  window.fetch = (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const path = sameOriginPath(input);
    if (path === null || path.startsWith(INGEST_PREFIX)) {
      return original(input, init);
    }
    const traceId = newTraceIdHex();
    const started = performance.now();
    const headers = new Headers(init?.headers ?? (input instanceof Request ? input.headers : {}));
    headers.set("traceparent", `00-${traceId}-${spanIdHex()}-01`);
    const method = (
      init?.method ?? (input instanceof Request ? input.method : "GET")
    ).toUpperCase();

    const done = (status: string) => {
      emitSpan(`${app}.rpc`, {
        "trace.id": traceId,
        "rpc.path": path,
        "rpc.method": method,
        "rpc.status": status,
        "rpc.duration_ms": String(Math.round(performance.now() - started)),
      });
    };
    return original(input, { ...init, headers }).then(
      (res) => {
        done(String(res.status));
        return res;
      },
      (err: unknown) => {
        done("network_error");
        reportError(err, { "error.op": "fetch", "trace.id": traceId, "rpc.path": path });
        throw err;
      },
    );
  };
}

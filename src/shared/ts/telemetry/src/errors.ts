// By-construction error capture: everything a page can surface
// asynchronously lands in the emitSpan queue as an `error` event — uncaught
// exceptions and resource load failures (capture-phase "error"), unhandled
// promise rejections, CSP violations — plus reportError() for code that
// must swallow an exception without swallowing the evidence. Every capture
// carries a fresh trace id (also returned to the caller) so one page's
// failure can be quoted verbatim and joined in ClickHouse. Noise from
// extensions and middleboxes is accepted by design; only unbounded
// repetition is capped, and the cap itself is reported.

import { emitSpan } from "./browser";
import { newTraceIdHex } from "./trace-id";

const PER_SIGNATURE_CAP = 8;
const PAGE_CAP = 80;

let app = "";
let installed = false;
let total = 0;
let suppressedReported = false;
const perSignature = new Map<string, number>();

interface Shape {
  type: string;
  message: string;
  stack: string;
}

function shapeOf(v: unknown): Shape {
  if (v instanceof Error) {
    return { type: v.name, message: v.message, stack: v.stack ?? "" };
  }
  if (typeof v === "object" && v !== null) {
    let json = "";
    try {
      json = JSON.stringify(v);
    } catch {
      json = Object.prototype.toString.call(v);
    }
    return { type: "object", message: json, stack: "" };
  }
  return { type: typeof v, message: String(v), stack: "" };
}

function record(kind: string, s: Shape, attrs: Record<string, string>): string {
  const id = newTraceIdHex();
  const sig = `${kind}|${s.message.slice(0, 120)}`;
  const seen = (perSignature.get(sig) ?? 0) + 1;
  perSignature.set(sig, seen);
  total++;
  if (seen > PER_SIGNATURE_CAP || total > PAGE_CAP) {
    // One terminal event marks that the page kept failing past the cap, so
    // a capped page never reads as a recovered one.
    if (!suppressedReported && total > PAGE_CAP) {
      suppressedReported = true;
      emitSpan("error", {
        app,
        "trace.id": id,
        "error.kind": "suppressed",
        "error.message": `error cap reached (${PAGE_CAP} this page load)`,
        "route.path": window.location.pathname,
      });
    }
    return id;
  }
  emitSpan("error", {
    app,
    "trace.id": id,
    "error.kind": kind,
    "error.type": s.type,
    "error.message": s.message.slice(0, 400),
    "error.stack": s.stack.slice(0, 1000),
    "error.repeat": String(seen),
    "route.path": window.location.pathname,
    ...attrs,
  });
  return id;
}

// reportError is the explicit lane: call it from catch blocks that handle
// an error (so nothing rethrows) but must still surface it. Returns the
// error's trace id (32-hex) for user-facing diagnostics.
export function reportError(err: unknown, attrs: Record<string, string> = {}): string {
  if (typeof window === "undefined") return "";
  return record("reported", shapeOf(err), attrs);
}

export function installErrorCapture(appName: string): void {
  if (installed || typeof window === "undefined") return;
  installed = true;
  app = appName;

  // Capture phase so resource elements' non-bubbling error events (script,
  // img, link, media) are seen along with uncaught runtime exceptions.
  window.addEventListener(
    "error",
    (e: Event) => {
      const target = e.target;
      if (target instanceof Element) {
        const el = target as Element & { src?: string; href?: string };
        record(
          "resource",
          {
            type: el.tagName.toLowerCase(),
            message: `failed to load ${el.tagName.toLowerCase()}: ${el.src ?? el.href ?? ""}`,
            stack: "",
          },
          {},
        );
        return;
      }
      const ee = e as ErrorEvent;
      const s =
        ee.error !== undefined && ee.error !== null
          ? shapeOf(ee.error)
          : { type: "error", message: ee.message ?? "unknown error", stack: "" };
      record("uncaught", s, {
        "error.source": `${ee.filename ?? ""}:${ee.lineno ?? 0}:${ee.colno ?? 0}`,
      });
    },
    true,
  );

  window.addEventListener("unhandledrejection", (e: PromiseRejectionEvent) => {
    record("unhandledrejection", shapeOf(e.reason), {});
  });

  document.addEventListener("securitypolicyviolation", (e: SecurityPolicyViolationEvent) => {
    record(
      "csp",
      {
        type: e.effectiveDirective,
        message: `${e.violatedDirective} blocked ${e.blockedURI}`,
        stack: "",
      },
      { "error.disposition": e.disposition },
    );
  });
}

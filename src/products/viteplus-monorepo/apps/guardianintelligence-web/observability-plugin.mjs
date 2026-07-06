import { createRequire as __nodeCreateRequire } from "node:module";
__nodeCreateRequire(import.meta.url);
import { trace } from "@opentelemetry/api";
import { definePlugin } from "nitro";
import { randomUUID } from "node:crypto";
import { defineEventHandler, getCookie, setCookie } from "nitro/h3";
//#region correlation
const correlationCookieName = "guardian_correlation_id";
const correlationContextKey = "guardian_correlation_id";
//#endregion
//#region correlation-middleware
const correlationMiddleware = defineEventHandler((event) => {
  let correlationID = (getCookie(event, "guardian_correlation_id") ?? "").trim();
  if (correlationID === "") {
    correlationID = randomUUID();
    setCookie(event, correlationCookieName, correlationID, {
      path: "/",
      sameSite: "lax",
      secure: true,
      httpOnly: false,
      maxAge: 3600 * 24 * 30,
    });
  }
  event.context[correlationContextKey] = correlationID;
});
//#endregion
//#region observability-plugin
const requestStartKey = "guardian_request_started_at_ns";
const clientIPHeaderName = "x-guardian-client-ip";
const edgePeerIPHeaderName = "x-guardian-edge-peer-ip";
const clientIPSourceHeaderName = "x-guardian-client-ip-source";
function correlationIDFromContext(value) {
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "bigint" || typeof value === "boolean")
    return String(value);
  return "";
}
function emitLog(level, msg, fields) {
  const payload = {
    time: /* @__PURE__ */ new Date().toISOString(),
    level,
    msg,
    ...fields,
  };
  const line = JSON.stringify(payload);
  if (level === "error") {
    console.error(line);
    return;
  }
  if (level === "warn") {
    console.warn(line);
    return;
  }
  console.log(line);
}
function responseStatusCode(response, event) {
  if (response !== null && typeof response === "object" && "status" in response) {
    const status = response.status;
    if (typeof status === "number" && status > 0) return status;
  }
  return event.res.status ?? 200;
}
function serverFnIDFromPath(pathname) {
  if (!pathname.startsWith("/_serverFn/")) return "";
  const tail = pathname.slice(11);
  const slash = tail.indexOf("/");
  return slash === -1 ? tail : tail.slice(0, slash);
}
var observability_plugin_default = definePlugin((nitroApp) => {
  nitroApp.hooks.hook("request", (event) => {
    const h3Event = event;
    correlationMiddleware(h3Event);
    const context = h3Event.context;
    context[requestStartKey] = process.hrtime.bigint();
    const span = trace.getActiveSpan();
    if (!span) return;
    const correlationID =
      h3Event.req.headers.get("X-Guardian-Correlation-Id".toLowerCase()) ??
      correlationIDFromContext(context["guardian_correlation_id"]);
    span.setAttribute("guardian.correlation_id", correlationID);
    span.setAttribute("guardian.route", h3Event.url.pathname);
    const clientIP = h3Event.req.headers.get(clientIPHeaderName) ?? "";
    if (clientIP !== "") {
      span.setAttribute("guardian.request.client_ip", clientIP);
      span.setAttribute("guardian.request.client_ip_trusted", true);
    }
    const edgePeerIP = h3Event.req.headers.get(edgePeerIPHeaderName) ?? "";
    if (edgePeerIP !== "") span.setAttribute("guardian.request.edge_peer_ip", edgePeerIP);
    const clientIPSource = h3Event.req.headers.get(clientIPSourceHeaderName) ?? "";
    if (clientIPSource !== "")
      span.setAttribute("guardian.request.client_ip_source", clientIPSource);
    const serverFnID = serverFnIDFromPath(h3Event.url.pathname);
    if (serverFnID !== "") span.setAttribute("guardian.server_fn", serverFnID);
  });
  nitroApp.hooks.hook("response", (response, event) => {
    const h3Event = event;
    const context = h3Event.context;
    const requestStartedAt = context[requestStartKey];
    const durationMs =
      typeof requestStartedAt === "bigint"
        ? Number((process.hrtime.bigint() - requestStartedAt) / 1000000n)
        : 0;
    const spanContext = trace.getActiveSpan()?.spanContext();
    const url = h3Event.url;
    const statusCode = responseStatusCode(response, h3Event);
    const level = statusCode >= 500 ? "error" : statusCode >= 400 ? "warn" : "info";
    const correlationID =
      h3Event.req.headers.get("X-Guardian-Correlation-Id".toLowerCase()) ??
      correlationIDFromContext(context["guardian_correlation_id"]);
    emitLog(level, "http request completed", {
      trace_id: spanContext?.traceId ?? "",
      span_id: spanContext?.spanId ?? "",
      guardian_correlation_id: correlationID,
      http_method: h3Event.req.method,
      http_target: `${url.pathname}${url.search}`,
      url_path: url.pathname,
      http_status_code: statusCode,
      duration_ms: durationMs,
      user_agent: h3Event.req.headers.get("user-agent") ?? "",
      client_ip: h3Event.req.headers.get(clientIPHeaderName) ?? "",
      edge_peer_ip: h3Event.req.headers.get(edgePeerIPHeaderName) ?? "",
      client_ip_source: h3Event.req.headers.get(clientIPSourceHeaderName) ?? "",
    });
  });
  nitroApp.hooks.hook("error", (error, context) => {
    const spanContext = trace.getActiveSpan()?.spanContext();
    const h3Event = context.event;
    const url = h3Event?.url;
    const correlationID =
      h3Event?.req.headers.get("X-Guardian-Correlation-Id".toLowerCase()) ??
      correlationIDFromContext(h3Event?.context?.["guardian_correlation_id"]);
    emitLog("error", "nitro request failed", {
      trace_id: spanContext?.traceId ?? "",
      span_id: spanContext?.spanId ?? "",
      guardian_correlation_id: correlationID,
      http_method: h3Event?.req.method ?? "",
      http_target: url ? `${url.pathname}${url.search}` : "",
      url_path: url?.pathname ?? "",
      client_ip: h3Event?.req.headers.get(clientIPHeaderName) ?? "",
      edge_peer_ip: h3Event?.req.headers.get(edgePeerIPHeaderName) ?? "",
      client_ip_source: h3Event?.req.headers.get(clientIPSourceHeaderName) ?? "",
      error_name: error.name,
      error_message: error.message,
      error_stack: error.stack ?? "",
    });
  });
});
//#endregion
export { observability_plugin_default as default };

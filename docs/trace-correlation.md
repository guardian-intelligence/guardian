# Following a user experience across the stack

Every product page runs `@guardian/telemetry` (mounted once as
`TelemetryProbe app="…"`), which gives each browser three identities and
makes one of them quotable:

| id | scope | minted by | where it lands |
|---|---|---|---|
| **trace id** (32-hex, W3C) | one logical action: an rpc, an error | browser (`fetch-telemetry.ts`, `errors.ts`) | `events.trace_id`, `otel_traces.TraceId`, service log lines |
| correlation id | visitor, 30 days | ingest HMAC cookie (server-derived) | `events.correlation_id`, span attr `guardian.correlation_id` |
| page id (16-hex) | one page load | browser (`browser.ts`, eager) | `events.page_id` |
| session_seq | order within a visit | browser sessionStorage counter | `events.session_seq` |

Every event also carries `event_ts` (client emission time mapped onto the
server clock via the batch skew; equals `server_ts` when the client sent
nothing plausible — `server_ts` alone is beacon-flush time, identical for a
whole batch) and `release` (short image digest of the frontend deployment
that served the page). Concurrent first flushes from a fresh visitor can
race the correlation cookie and land under different correlation ids;
`page_id` is the join that survives that split:

```sql
SELECT event_ts, event_name, session_seq, release, left(props, 200)
FROM guardian_analytics.events
WHERE site = 'wakeupmythra.com' AND page_id = unhex('P')  -- 16 hex chars
ORDER BY session_seq;
```

The trace id is the handle users (and the WUM HUD log: `[err <id>]`) can
quote. Same-origin fetches carry it as `traceparent`; server middlewares
(`src/services/telemetry`) continue it, so the browser-minted id IS the
server span's TraceId — no joins through intermediate ids.

## The cookbook

Reach ClickHouse, then work with a trace id `T` (32 hex chars):

```sh
kubectl port-forward -n tenant-root svc/chendpoint-clickhouse-analytics 9000:9000
kubectl get secret -n guardian-analytics analytics-ch-ingest -o jsonpath='{.data.ingest}' | base64 -d
clickhouse-client --host 127.0.0.1 --user ingest
```

`SHOW CREATE TABLE guardian_analytics.events` / `guardian_analytics.otel_traces`
gives the schema actually being served; the source is
`src/infrastructure/deployments/analytics/system/{ddl-configmap.yaml,traces-configmap.yaml}`.

```sql
-- What the browser saw: the event carrying this id, and the rest of that
-- visitor's session around it.
SELECT server_ts, site, event_name, path, session_seq, props
FROM guardian_analytics.events
WHERE trace_id = unhex('T')
ORDER BY server_ts;

SELECT server_ts, event_name, path, session_seq, left(props, 200)
FROM guardian_analytics.events
WHERE (site, correlation_id) IN (
  SELECT site, correlation_id FROM guardian_analytics.events
  WHERE trace_id = unhex('T') LIMIT 1)
ORDER BY session_seq;

-- What the services did under the same id (narrow by time first via the
-- lookup table; both live in guardian_analytics).
SELECT Start, End FROM guardian_analytics.otel_traces_trace_id_ts
WHERE TraceId = 'T';

SELECT Timestamp, ServiceName, SpanName, Duration / 1e6 AS ms,
       StatusCode, SpanAttributes
FROM guardian_analytics.otel_traces
WHERE TraceId = 'T'
ORDER BY Timestamp;
```

Log lines that carry a trace id print it as `trace_id=T` (e.g. mythrad's
`session minted` line), so VictoriaLogs closes the loop:

```
_msg:trace_id=T
```

## Error events

Every uncaught exception, unhandled rejection, resource load failure, CSP
violation, and explicit `reportError()` is an `error` event with its own
fresh trace id, `error.kind`, message, and truncated stack in `props`.
Slice by deployment (the `release` column) or app:

```sql
SELECT event_ts, site, release,
       JSONExtractString(props, 'error.kind') AS kind,
       JSONExtractString(props, 'error.message') AS message
FROM guardian_analytics.events
WHERE event_name = 'error' AND server_ts > now() - INTERVAL 1 DAY
ORDER BY server_ts DESC;
```

Netcode health rides the same pipeline: `wum.connected` (every successful
WebTransport dial, with `wum.dial_ms`; its absence after a `wum.route_view`
means the page never got in — a hung handshake times out after 10s and
reports through the error path), `wum.netcode_reject`, `wum.netcode_resync`,
`wum.netcode_mismatch`, and `wum.redial`. Server-side the same session shows
as mythrad `wt session open` / `wt session close` log lines (sub, role,
park, remote, close reason, duration) plus the per-reason
`mythra_intents_rejected_total` metric (alert: `MythradIntentRejectSpike`).

## What deliberately does NOT join

- The analytics ingest treats a client `traceparent` as a *link*, never a
  parent (`/api/events` is public and unauthenticated; an attacker must
  not choose our span parents). Its spans have their own TraceId with the
  visitor's correlation id as an attribute.
- Cross-origin fetches (Keycloak brokers, Stripe) are not instrumented —
  a traceparent header would force CORS preflights on third parties.

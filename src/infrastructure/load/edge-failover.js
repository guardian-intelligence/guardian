import http from "k6/http";
import { sleep } from "k6";

const targetURL = __ENV.EDGE_FAILOVER_URL;
const expectedStatuses = (__ENV.EDGE_FAILOVER_EXPECTED_STATUSES || "200")
  .split(",")
  .map((value) => Number(value.trim()))
  .filter((value) => !Number.isNaN(value));
const intervalMs = Number(__ENV.EDGE_FAILOVER_INTERVAL_MS || "250");

http.setResponseCallback(http.expectedStatuses(...expectedStatuses));

export const options = {
  scenarios: {
    edge_failover: {
      executor: "constant-vus",
      vus: 1,
      duration: __ENV.EDGE_FAILOVER_DURATION || "5m",
    },
  },
  noConnectionReuse: true,
  noVUConnectionReuse: true,
  thresholds: {
    http_reqs: ["count>0"],
  },
  summaryTrendStats: ["avg", "min", "med", "p(95)", "p(99)", "max"],
};

export default function () {
  const startedMs = Date.now();
  const response = http.get(targetURL, {
    redirects: 0,
    responseType: "none",
    timeout: __ENV.EDGE_FAILOVER_REQUEST_TIMEOUT || "5s",
    tags: {
      name: __ENV.EDGE_FAILOVER_REQUEST_NAME || "guardian-edge-failover",
      guardian_surface: __ENV.GUARDIAN_SURFACE || "edge",
      guardian_stage: __ENV.GUARDIAN_STAGE || "root",
    },
  });
  const statusOk = expectedStatuses.includes(response.status);
  const cfRayPresent = hasHeader(response.headers, "cf-ray");
  console.log(
    JSON.stringify({
      event: "guardian_edge_failover_sample",
      time_unix_ms: startedMs,
      duration_ms: response.timings.duration,
      status: response.status,
      ok: statusOk && cfRayPresent,
      status_ok: statusOk,
      cf_ray_present: cfRayPresent,
      error: response.error || "",
    }),
  );
  sleep(intervalMs / 1000);
}

function hasHeader(headers, name) {
  const want = name.toLowerCase();
  return Object.keys(headers || {}).some((key) => key.toLowerCase() === want);
}

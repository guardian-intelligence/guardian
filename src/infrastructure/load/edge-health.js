import http from "k6/http";
import { check, sleep } from "k6";
import { Counter, Rate } from "k6/metrics";

const targets = JSON.parse(__ENV.EDGE_TARGETS_JSON || "[]");
const hostOverrides = parseHostOverrides(__ENV.EDGE_K6_HOSTS || "");
const iterations = Number(__ENV.EDGE_K6_ITERATIONS || "2");
const minRequests = Number(
  __ENV.EDGE_K6_MIN_REQUESTS || String(targets.length * iterations),
);
const expectedStatuses = unique(
  targets.flatMap((target) => target.expected_statuses || [200]),
);
const targetRequests = new Counter("guardian_edge_target_requests");
const expectedStatusRate = new Rate("guardian_edge_expected_status");

http.setResponseCallback(http.expectedStatuses(...expectedStatuses));

export const options = {
  scenarios: {
    default: {
      executor: "shared-iterations",
      vus: Number(__ENV.EDGE_K6_VUS || "1"),
      iterations,
      maxDuration: __ENV.EDGE_K6_MAX_DURATION || "2m",
    },
  },
  hosts: hostOverrides,
  noConnectionReuse: true,
  noVUConnectionReuse: true,
  thresholds: {
    guardian_edge_target_requests: [`count>=${minRequests}`],
    guardian_edge_expected_status: [
      __ENV.EDGE_K6_EXPECTED_STATUS_THRESHOLD || "rate>0.99",
    ],
    http_reqs: ["count>0"],
  },
  summaryTrendStats: ["avg", "min", "med", "p(95)", "p(99)", "max"],
};

export default function () {
  for (const target of targets) {
    targetRequests.add(1, {
      guardian_surface: target.surface,
      guardian_stage: target.stage,
      guardian_origin: __ENV.EDGE_K6_ORIGIN || "public-dns",
    });
    const response = http.get(target.url, {
      redirects: 0,
      responseType: "none",
      timeout: __ENV.EDGE_K6_REQUEST_TIMEOUT || "10s",
      tags: {
        name: target.name,
        guardian_surface: target.surface,
        guardian_stage: target.stage,
        guardian_origin: __ENV.EDGE_K6_ORIGIN || "public-dns",
      },
    });
    const ok = (target.expected_statuses || [200]).includes(response.status);
    if (!ok) {
      console.error(
        JSON.stringify({
          msg: "guardian edge probe failed",
          url: target.url,
          surface: target.surface,
          stage: target.stage,
          origin: __ENV.EDGE_K6_ORIGIN || "public-dns",
          status: response.status,
          error: response.error || "",
          expected_statuses: target.expected_statuses || [200],
        }),
      );
    }
    expectedStatusRate.add(ok, {
      guardian_surface: target.surface,
      guardian_stage: target.stage,
      guardian_origin: __ENV.EDGE_K6_ORIGIN || "public-dns",
    });
    check(response, {
      "status is expected": () => ok,
    });
  }
  sleep(Number(__ENV.EDGE_K6_SLEEP_SECONDS || "1"));
}

function parseHostOverrides(value) {
  const hosts = {};
  for (const rawEntry of value.split(",")) {
    const entry = rawEntry.trim();
    if (entry === "") {
      continue;
    }
    const separator = entry.indexOf("=");
    if (separator <= 0 || separator === entry.length - 1) {
      throw new Error(`invalid EDGE_K6_HOSTS entry: ${entry}`);
    }
    hosts[entry.slice(0, separator).trim()] = entry.slice(separator + 1).trim();
  }
  return hosts;
}

function unique(values) {
  return Array.from(new Set(values)).sort((left, right) => left - right);
}

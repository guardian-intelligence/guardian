import http from "k6/http";
import { check, sleep } from "k6";

const targetURL = __ENV.TARGET_URL;
const expectedStatuses = (__ENV.EXPECTED_STATUSES || "200")
  .split(",")
  .map((value) => Number(value.trim()))
  .filter((value) => !Number.isNaN(value));
const requestName = __ENV.REQUEST_NAME || "guardian-http-load";
const hostOverrides = parseHostOverrides(__ENV.K6_HOSTS || "");

http.setResponseCallback(http.expectedStatuses(...expectedStatuses));

export const options = {
  vus: Number(__ENV.K6_VUS || "1"),
  duration: __ENV.K6_DURATION || "30s",
  hosts: hostOverrides,
  thresholds: {
    http_req_failed: [__ENV.K6_HTTP_REQ_FAILED_THRESHOLD || "rate<0.01"],
    http_reqs: ["count>0"],
  },
  summaryTrendStats: ["avg", "min", "med", "p(95)", "p(99)", "max"],
};

export default function () {
  const response = http.get(targetURL, {
    tags: {
      name: requestName,
      guardian_surface: __ENV.GUARDIAN_SURFACE || "custom",
      guardian_stage: __ENV.GUARDIAN_STAGE || "custom",
    },
  });

  check(response, {
    "status is expected": (r) => expectedStatuses.includes(r.status),
  });

  sleep(Number(__ENV.K6_SLEEP_SECONDS || "1"));
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
      throw new Error(`invalid K6_HOSTS entry: ${entry}`);
    }
    hosts[entry.slice(0, separator).trim()] = entry.slice(separator + 1).trim();
  }
  return hosts;
}

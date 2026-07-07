// Deploy-time company-site stress gate.
//
// Flagger runs this against the beta canary-color Service with:
//   --tag stage=beta --tag surface=company-site --tag scenario=stress
// Local runs must add --tag run_source=local so gate queries never see them.
import http from 'k6/http';

const target = (__ENV.TARGET_URL || 'http://company-site-canary.tenant-guardian-beta').replace(
  /\/+$/,
  '',
);

const requests = [
  { path: '/healthz', name: 'GET /healthz' },
  { path: '/', name: 'GET /' },
  { path: '/letters', name: 'GET /letters' },
  { path: '/letters/dear-shovon', name: 'GET /letters/dear-shovon' },
];

export const options = {
  // 250 rps is the calibrated sustainable rate across all stages (~21M req/day
  // equivalent). 500 rps saturated the 245KB /letters/dear-shovon route on the
  // gamma/prod canary — its bandwidth share cascaded into VU exhaustion — while
  // /, /letters and /healthz stayed sub-10ms. At 250 rps the full mix holds
  // p95 ~5ms with zero drops on every stage.
  scenarios: {
    stress: {
      executor: 'ramping-arrival-rate',
      timeUnit: '1s',
      preAllocatedVUs: 50,
      maxVUs: 200,
      stages: [
        { duration: '30s', target: 250 },
        { duration: '90s', target: 250 },
      ],
    },
  },
  // Observe-only soak: http_req_failed and dropped_iterations are
  // calibration-free invariants (a static site serving >1% errors, or a
  // generator that can't sustain the arrival rate, is wrong at any scale), so
  // they hard-fail the deploy gate now. The latency budget is a guess until a
  // week of stress data sets it; p95 is still remote-written for calibration
  // but is NOT gated yet. Re-add `http_req_duration: ['p(95)<NNN']` here once
  // calibrated to flip latency to blocking. See docs/loadtest.md.
  thresholds: {
    http_req_failed: ['rate<0.01'],
    dropped_iterations: ['count<1'],
  },
};

export default function () {
  const request = requests[Math.floor(Math.random() * requests.length)];
  http.get(`${target}${request.path}`, { tags: { name: request.name } });
}

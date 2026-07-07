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
  scenarios: {
    stress: {
      executor: 'ramping-arrival-rate',
      timeUnit: '1s',
      preAllocatedVUs: 250,
      maxVUs: 750,
      stages: [
        { duration: '30s', target: 500 },
        { duration: '90s', target: 500 },
      ],
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<250'],
    dropped_iterations: ['count<1'],
  },
};

export default function () {
  const request = requests[Math.floor(Math.random() * requests.length)];
  http.get(`${target}${request.path}`, { tags: { name: request.name } });
}

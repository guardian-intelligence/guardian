// During-analysis canary load for the prod weighted rollout.
//
// Runs through the PUBLIC edge (Cloudflare -> origin nginx): the canary
// weight split happens inside the ingress controller and AOP admits only
// edge-terminated connections, so in-cluster traffic can neither reach the
// split nor pass mTLS. A unique query param defeats edge caching so every
// request reaches the origin and lands in the per-service response series
// the canary gate reads.
import http from 'k6/http';

if (!__ENV.TARGET_URL) {
  throw new Error('TARGET_URL is required (e.g. https://guardianintelligence.org)');
}
const target = __ENV.TARGET_URL.replace(/\/+$/, '');

const paths = ['/healthz', '/', '/letters', '/letters/dear-shovon'];

export const options = {
  // 10 rps is a volume floor for the gate's per-minute windows (~60 canary
  // responses at the 10% step), not a stress test — stress.js already
  // asserted 250 rps against the canary color before any traffic shifted.
  scenarios: {
    'canary-load': {
      executor: 'constant-arrival-rate',
      rate: 10,
      timeUnit: '1s',
      // Covers four 1m weight steps plus promotion slack; the webhook
      // re-fires each interval and the loadtester skips re-launch while
      // this run is still going.
      duration: '6m',
      preAllocatedVUs: 10,
      maxVUs: 30,
    },
  },
  // The Flagger metric checks are the gate; this generator only guarantees
  // request volume, so only generator health hard-fails here.
  thresholds: {
    dropped_iterations: ['count<1'],
  },
};

export default function () {
  const path = paths[Math.floor(Math.random() * paths.length)];
  const bust = Math.random().toString(36).slice(2);
  http.get(`${target}${path}?cb=${bust}`, { tags: { name: `GET ${path}` } });
}

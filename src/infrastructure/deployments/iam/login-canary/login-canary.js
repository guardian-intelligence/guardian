// Minutely synthetic login canary. Three flows, per Shovon's directive
// (2026-07-05), synthetic until a real GitHub test account exists — the full
// browser OOBE canary (real GitHub login + API-driven state wipe including
// the GitHub grant) replaces flow coverage here later:
//   1. existing-user happy path — direct-grant login as the permanent canary
//      realm user against the in-cluster apex Service (the exact primary
//      path users hit, minus the edge, which edge-health covers);
//   2. new-user happy path — create a fresh user via the admin API, first
//      login, land on the Postflight OOBE placeholder page, then wipe the user
//      (the wipe is what makes every run a true first-time signup);
//   3. cancellation — the OIDC auth page must render with the GitHub IdP
//      link, and a brokered cancel callback (GitHub redirecting back with
//      error=access_denied) must be handled gracefully, never a 5xx.
// Results remote-write to VictoriaMetrics as k6_canary_* series tagged with
// stage; VMRule alerts live in <stage>/observability.yaml. Rendered into the
// per-stage keycloak-login-canary ConfigMap by each stage's
// configMapGenerator.
//
// The new-user flow uses the bootstrap-admin credential (already present in
// the stage namespace) — narrow to a dedicated service-account client with
// only manage-users once realm config management (post-import upserts) lands.
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const failures = new Counter('canary_failures');
const successes = new Counter('canary_success');

export const options = {
  iterations: 1,
  thresholds: { canary_failures: ['count<1'] },
};

const KC = __ENV.KC_INTERNAL;
const PUB = __ENV.PUBLIC_BASE;
const OOBE = __ENV.OOBE_URL;

function flow(name, fn) {
  let ok = false;
  try {
    ok = fn();
  } catch (e) {
    console.error(`${name}: ${e}`);
  }
  (ok ? successes : failures).add(1, { flow: name });
}

function tokenRequest(realmURL, body) {
  return http.post(`${realmURL}/protocol/openid-connect/token`, body);
}

function existingUser() {
  const res = tokenRequest(`${KC}/realms/postflight`, {
    grant_type: 'password',
    client_id: 'canary',
    username: __ENV.CANARY_USERNAME,
    password: __ENV.CANARY_PASSWORD,
  });
  return check(res, {
    'existing-user: token issued': (r) =>
      r.status === 200 && r.json('access_token') !== undefined,
  });
}

function adminHeaders() {
  const res = tokenRequest(`${KC}/realms/master`, {
    grant_type: 'password',
    client_id: 'admin-cli',
    username: __ENV.KC_ADMIN_USERNAME,
    password: __ENV.KC_ADMIN_PASSWORD,
  });
  if (res.status !== 200) throw new Error(`admin token: ${res.status}`);
  return {
    headers: {
      Authorization: `Bearer ${res.json('access_token')}`,
      'Content-Type': 'application/json',
    },
  };
}

function wipeNewUser(base, auth) {
  const found = http.get(`${base}/users?username=canary-newuser&exact=true`, auth);
  // Pin the URL-grouping tag: without it every per-UUID delete mints a new
  // VictoriaMetrics series across the whole k6_http_* family (~1440/day/stage).
  const del = { ...auth, tags: { name: `${base}/users/{id}` } };
  let wiped = true;
  for (const u of found.json() || []) {
    wiped = http.del(`${base}/users/${u.id}`, null, del).status === 204 && wiped;
  }
  return wiped;
}

function newUser() {
  const auth = adminHeaders();
  const base = `${KC}/admin/realms/postflight`;
  wipeNewUser(base, auth); // clear residue from any previous failed run
  const created = http.post(
    `${base}/users`,
    JSON.stringify({
      username: 'canary-newuser',
      enabled: true,
      emailVerified: true,
      firstName: 'Synthetic',
      lastName: 'NewUser',
      email: 'canary-newuser@guardianintelligence.org',
      credentials: [
        { type: 'password', value: __ENV.CANARY_PASSWORD, temporary: false },
      ],
    }),
    auth,
  );
  const login = tokenRequest(`${KC}/realms/postflight`, {
    grant_type: 'password',
    client_id: 'canary',
    username: 'canary-newuser',
    password: __ENV.CANARY_PASSWORD,
  });
  const oobe = http.get(OOBE);
  const wiped = wipeNewUser(base, auth);
  return check(
    { created, login, oobe, wiped },
    {
      'new-user: created': (o) => o.created.status === 201,
      'new-user: first login': (o) =>
        o.login.status === 200 && o.login.json('access_token') !== undefined,
      'new-user: OOBE page served': (o) =>
        o.oobe.status === 200 && o.oobe.body.includes('data-postflight-oobe'),
      'new-user: state wiped': (o) => o.wiped === true,
    },
  );
}

function cancellation() {
  const authPage = http.get(
    `${PUB}/realms/postflight/protocol/openid-connect/auth` +
      `?client_id=postflight-web&redirect_uri=${encodeURIComponent(OOBE)}` +
      `&response_type=code&scope=openid`,
  );
  const cancel = http.get(
    `${PUB}/realms/postflight/broker/github/endpoint?error=access_denied&state=synthetic-cancel`,
  );
  return check(
    { authPage, cancel },
    {
      'cancellation: auth page renders with GitHub IdP': (o) =>
        o.authPage.status === 200 && o.authPage.body.includes('broker/github'),
      'cancellation: brokered cancel is not a server error': (o) =>
        o.cancel.status < 500,
    },
  );
}

export default function () {
  flow('existing-user', existingUser);
  flow('new-user', newUser);
  flow('cancellation', cancellation);
}

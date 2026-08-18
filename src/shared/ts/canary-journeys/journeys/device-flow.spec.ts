import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { loadJourneyConfig, type JourneyConfig } from "../src/config.ts";
import {
  awaitGitHubRedirect,
  awaitPostflightLanding,
  signInAtGitHub,
  step,
} from "../src/github-flow.ts";
import { SELECTORS } from "../src/probes.ts";

interface DeviceAuthorization {
  device_code: string;
  user_code: string;
  interval?: number;
}

async function startDeviceFlow(
  request: APIRequestContext,
  cfg: JourneyConfig,
): Promise<DeviceAuthorization> {
  const response = await request.post(`${cfg.issuer}/protocol/openid-connect/auth/device`, {
    // The scope the CLI asks for, because the token this mints has to be the
    // token the CLI holds: without `openid` the issuer mints a plain OAuth
    // access token and userinfo — the CLI's whole liveness check — answers 403
    // "Missing openid scope" no matter how healthy the session is.
    form: { client_id: "postflight-cli", scope: "openid" },
  });
  expect(response.status()).toBe(200);
  const body = (await response.json()) as DeviceAuthorization;
  expect(body.user_code).toBeTruthy();
  expect(body.device_code).toBeTruthy();
  return body;
}

async function pollDeviceToken(
  request: APIRequestContext,
  cfg: JourneyConfig,
  deviceCode: string,
): Promise<{
  status: number;
  body: { access_token?: string; refresh_token?: string; error?: string };
}> {
  const response = await request.post(`${cfg.issuer}/protocol/openid-connect/token`, {
    form: {
      client_id: "postflight-cli",
      grant_type: "urn:ietf:params:oauth:grant-type:device_code",
      device_code: deviceCode,
    },
  });
  return { status: response.status(), body: await response.json() };
}

// The question `postflight auth status` asks on every run: does this token
// still name a session.
async function userInfoStatus(
  request: APIRequestContext,
  cfg: JourneyConfig,
  accessToken: string,
): Promise<number> {
  const response = await request.get(`${cfg.issuer}/protocol/openid-connect/userinfo`, {
    headers: { Authorization: `Bearer ${accessToken}` },
  });
  return response.status();
}

// Clicks Approve on the device page and rides the flow to GitHub, returning
// the broker `state` Keycloak minted for this browser context. The state is
// carried in GitHub's authorize URL directly, or nested in the login page's
// return_to when the context holds no GitHub session.
async function approveUntilGitHub(
  page: Page,
  cfg: JourneyConfig,
  userCode: string,
): Promise<string> {
  await page.goto(`${cfg.pageUrl.replace(/\/$/, "")}/device?user_code=${userCode}`);
  const code = page.locator("#device-user-code");
  await expect(code).toHaveValue(userCode);
  await Promise.all([
    page.waitForURL(/github\.com/, { timeout: 20_000 }),
    page.locator("#postflight-device-approve").click(),
  ]);
  const url = new URL(page.url());
  let state = url.pathname.includes("/login/oauth/authorize")
    ? url.searchParams.get("state")
    : null;
  if (!state) {
    const returnTo = url.searchParams.get("return_to");
    if (returnTo) {
      state = new URL(returnTo, "https://github.com").searchParams.get("state");
    }
  }
  expect(state, "broker state visible in the GitHub URL").toBeTruthy();
  return state as string;
}

// The edge a real user hit in production: the approval starts in one browser
// context (a private window) and GitHub's redirect lands in another (their
// main browser), which holds none of the flow's cookies. Keycloak restarts
// the flow with loginTimeout, and the bounce theme must present that as an
// interruption to retry — never as GitHub denying access, and never as a
// rendered Keycloak page.
test("device approval interrupted across browser contexts lands on the retry surface", async ({
  browser,
  request,
}) => {
  const cfg = loadJourneyConfig(process.env);
  test.setTimeout(cfg.timeoutMs);

  step("device-code");
  const device = await startDeviceFlow(request, cfg);

  step("approve-context-a");
  const contextA = await browser.newContext();
  const stateA = await approveUntilGitHub(await contextA.newPage(), cfg, device.user_code);

  step("approve-context-b");
  const contextB = await browser.newContext();
  const pageB = await contextB.newPage();
  await approveUntilGitHub(pageB, cfg, device.user_code);

  step("replay-foreign-state");
  await pageB.goto(
    `${cfg.issuer}/broker/github/endpoint?state=${encodeURIComponent(stateA)}&code=canary-replay`,
  );
  await awaitPostflightLanding(
    pageB,
    "/postflight",
    '[data-auth-error="interrupted"]',
    "interrupted landing",
  );

  step("device-code-still-pending");
  const poll = await pollDeviceToken(request, cfg, device.device_code);
  expect(poll.status).toBe(400);
  expect(poll.body.error).toBe("authorization_pending");

  await contextA.close();
  await contextB.close();
});

// The bounce theme is the only place device-flow failures get their names:
// Keycloak's terminal pages share one "failed" header and the theme
// discriminates the body message into the product's ?error= vocabulary. Pin
// the two outcomes a person can cause — deny and expiry — which need no
// brokered login because the status page renders for any visitor.
test("device terminal failures land on their own product errors", async ({ page }) => {
  const cfg = loadJourneyConfig(process.env);
  test.setTimeout(cfg.timeoutMs);

  step("denied-bounce");
  await page.goto(`${cfg.issuer}/device/status?error=access_denied`);
  await awaitPostflightLanding(
    page,
    "/postflight/device",
    '[data-device-error="denied"]',
    "denied landing",
  );

  step("expired-bounce");
  await page.goto(`${cfg.issuer}/device/status?error=expired_token`);
  await awaitPostflightLanding(
    page,
    "/postflight/device",
    '[data-device-error="expired"]',
    "expired landing",
  );
});

test("device approval signs the CLI in end to end", async ({ page, request }) => {
  const cfg = loadJourneyConfig(process.env);
  test.setTimeout(cfg.timeoutMs);

  step("device-code");
  const device = await startDeviceFlow(request, cfg);

  step("open-approval-page");
  await page.goto(`${cfg.pageUrl.replace(/\/$/, "")}/device?user_code=${device.user_code}`);
  await expect(page.locator("#device-user-code")).toHaveValue(device.user_code);
  step("click-approve");
  await page.locator("#postflight-device-approve").click();
  step("await-github-redirect");
  await awaitGitHubRedirect(page);

  step("github-sign-in");
  await signInAtGitHub(page, cfg);

  step("device-done-landing");
  await awaitPostflightLanding(
    page,
    "/postflight/device/done",
    "[data-device-done]",
    "device done landing",
  );

  step("cli-token-issued");
  const deadline = Date.now() + 30_000;
  let outcome = await pollDeviceToken(request, cfg, device.device_code);
  while (outcome.status !== 200 && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, (device.interval ?? 5) * 1000));
    outcome = await pollDeviceToken(request, cfg, device.device_code);
  }
  expect(outcome.status).toBe(200);
  const accessToken = outcome.body.access_token as string;
  const refreshToken = outcome.body.refresh_token as string;
  expect(accessToken).toBeTruthy();
  expect(refreshToken, "the CLI needs a refresh token to sign itself out").toBeTruthy();

  // What `postflight auth status` and `postflight auth logout` rest on. The
  // CLI's own tests cover the requests it makes; only a canary holding a real
  // session can say the issuer still answers them the way the CLI reads — that
  // a live token names a session, and that signing out ends that session on the
  // server rather than deleting a file on someone's laptop.
  step("cli-session-live");
  expect(await userInfoStatus(request, cfg, accessToken)).toBe(200);

  step("cli-session-ended");
  const ended = await request.post(`${cfg.issuer}/protocol/openid-connect/logout`, {
    form: { client_id: "postflight-cli", refresh_token: refreshToken },
  });
  expect(ended.status()).toBeLessThan(300);
  expect(await userInfoStatus(request, cfg, accessToken)).toBe(401);

  step("cli-refresh-token-dead");
  const replayed = await request.post(`${cfg.issuer}/protocol/openid-connect/token`, {
    form: {
      client_id: "postflight-cli",
      grant_type: "refresh_token",
      refresh_token: refreshToken,
    },
  });
  expect(replayed.status()).toBe(400);
  expect((await replayed.json()).error).toBe("invalid_grant");

  step("logout");
  // Logout must be a same-origin navigation: a direct address-bar visit
  // carries sec-fetch-site: none, which the CSRF guard refuses.
  await page.goto(cfg.pageUrl);
  await page.locator(SELECTORS.signOut).click();
  await page.locator(SELECTORS.signIn).waitFor({ timeout: 20_000 });
  const loggedOutStatus = await page.evaluate(async () => {
    const response = await fetch("/postflight/auth/session", {
      credentials: "same-origin",
    });
    return response.status;
  });
  expect(loggedOutStatus).toBe(401);
});

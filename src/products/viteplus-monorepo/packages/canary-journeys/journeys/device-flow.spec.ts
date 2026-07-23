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
    form: { client_id: "postflight-cli" },
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
): Promise<{ status: number; body: { access_token?: string; error?: string } }> {
  const response = await request.post(`${cfg.issuer}/protocol/openid-connect/token`, {
    form: {
      client_id: "postflight-cli",
      grant_type: "urn:ietf:params:oauth:grant-type:device_code",
      device_code: deviceCode,
    },
  });
  return { status: response.status(), body: await response.json() };
}

// Clicks Approve on the device page and rides the flow to GitHub, returning
// the broker `state` Keycloak minted for this browser context. The state is
// carried in GitHub's authorize URL directly, or nested in the login page's
// return_to when the context holds no GitHub session.
async function approveUntilGitHub(page: Page, cfg: JourneyConfig, userCode: string): Promise<string> {
  await page.goto(`${cfg.pageUrl.replace(/\/$/, "")}/device?user_code=${userCode}`);
  const code = page.locator("#device-user-code");
  await expect(code).toHaveValue(userCode);
  await Promise.all([
    page.waitForURL(/github\.com/, { timeout: 20_000 }),
    page.locator("#postflight-device-approve").click(),
  ]);
  const url = new URL(page.url());
  let state = url.pathname.includes("/login/oauth/authorize") ? url.searchParams.get("state") : null;
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
  expect(outcome.body.access_token).toBeTruthy();

  step("logout");
  await page.goto(`${cfg.pageUrl.replace(/\/$/, "")}/auth/logout`);
  await awaitPostflightLanding(page, "/postflight", SELECTORS.oobeReady, "logout landing");
  const loggedOutStatus = await page.evaluate(async () => {
    const response = await fetch("/postflight/auth/session", {
      credentials: "same-origin",
    });
    return response.status;
  });
  expect(loggedOutStatus).toBe(401);
});

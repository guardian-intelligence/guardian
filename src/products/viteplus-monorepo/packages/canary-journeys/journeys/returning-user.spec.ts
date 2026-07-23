import { expect, test } from "@playwright/test";
import { loadJourneyConfig } from "../src/config.ts";
import {
  awaitGitHubRedirect,
  awaitPostflightLanding,
  signInAtGitHub,
  step,
} from "../src/github-flow.ts";
import { SELECTORS } from "../src/probes.ts";

interface SessionEnvelope {
  status: number;
  body: {
    authenticated: boolean;
    user?: { subject?: string; username?: string };
  };
}

test("returning user signs in through GitHub, reaches the console, and signs out", async ({
  page,
}) => {
  const cfg = loadJourneyConfig(process.env);
  test.setTimeout(cfg.timeoutMs);

  step("open-postflight");
  await page.goto(cfg.pageUrl);
  step("click-sign-in");
  await page.locator(SELECTORS.signIn).click();
  step("await-github-redirect");
  await awaitGitHubRedirect(page);

  step("github-sign-in");
  await signInAtGitHub(page, cfg);

  step("console-landing");
  await awaitPostflightLanding(
    page,
    "/postflight/console",
    SELECTORS.consoleReady,
    "Postflight console landing",
  );
  const session = await page.evaluate(async (): Promise<SessionEnvelope> => {
    const response = await fetch("/postflight/auth/session", {
      credentials: "same-origin",
    });
    return { status: response.status, body: await response.json() };
  });
  expect(session.status).toBe(200);
  expect(session.body.authenticated).toBe(true);
  expect(session.body.user?.subject).toBeTruthy();
  expect(session.body.user?.username).toBeTruthy();

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

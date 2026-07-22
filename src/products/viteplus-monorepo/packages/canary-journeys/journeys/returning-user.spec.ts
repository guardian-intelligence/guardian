import { expect, test, type Page } from "@playwright/test";
import { classifyOAuthPage } from "../src/classify.ts";
import { loadJourneyConfig, type JourneyConfig } from "../src/config.ts";
import { PROBE_SELECTORS, SELECTORS, oauthPageProbe } from "../src/probes.ts";
import { totp, totpBoundaryDelayMs } from "../src/totp.ts";

// Step markers reach the pod log through the reporter's stdout forwarding;
// a hung step is then named by the last marker emitted.
function step(name: string): void {
  process.stdout.write(`${JSON.stringify({ event: "step", name })}\n`);
}

interface SessionEnvelope {
  status: number;
  body: {
    authenticated: boolean;
    user?: { subject?: string; username?: string };
  };
}

async function awaitGitHubRedirect(page: Page): Promise<void> {
  await expect
    .poll(
      async () => {
        const state = await page.evaluate(oauthPageProbe, PROBE_SELECTORS);
        if (state.hasKeycloakPage) {
          throw new Error("Keycloak rendered a page instead of redirecting to GitHub");
        }
        return state.host;
      },
      { timeout: 20_000, intervals: [250] },
    )
    .toBe("github.com");
}

// GitHub's two-factor input auto-submits once the sixth digit lands, and a
// granted consent page can navigate mid-click: the explicit click then races
// the navigation it (or the fill before it) already caused. Best-effort
// click — if the control is gone or the page has moved on, the state
// ladder's next probe decides what happens.
async function clickIfPresent(page: Page, selector: string): Promise<void> {
  try {
    await page.locator(selector).first().click({ timeout: 3_000 });
  } catch {
    // Raced a navigation or the control disappeared; the ladder continues.
  }
}

async function finishGitHubAuthorization(page: Page, cfg: JourneyConfig): Promise<void> {
  const deadline = Date.now() + 75_000;
  let totpSent = false;
  let grantSent = false;
  while (Date.now() < deadline) {
    const state = await page.evaluate(oauthPageProbe, PROBE_SELECTORS);
    const action = classifyOAuthPage(state, cfg.guardianHost);
    switch (action) {
      case "complete":
        return;
      case "submit-totp": {
        if (totpSent) {
          await page.waitForTimeout(250);
          break;
        }
        const delay = totpBoundaryDelayMs(new Date());
        if (delay > 0) {
          await page.waitForTimeout(delay);
        }
        const code = totp(cfg.githubTotpSeed, new Date());
        await page.locator(SELECTORS.totpInput).first().fill(code);
        await clickIfPresent(page, SELECTORS.githubSubmit);
        totpSent = true;
        break;
      }
      case "grant": {
        if (grantSent) {
          await page.waitForTimeout(250);
          break;
        }
        await clickIfPresent(page, SELECTORS.grantEnabled);
        grantSent = true;
        break;
      }
      case "wait":
        await page.waitForTimeout(500);
        break;
    }
  }
  throw new Error("GitHub OAuth flow did not return to Postflight");
}

async function awaitPostflightLanding(
  page: Page,
  wantPath: string,
  marker: string,
  step: string,
): Promise<void> {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    const state = await page.evaluate(
      (args: { marker: string; keycloakPage: string }) => ({
        path: location.pathname,
        ready: Boolean(document.querySelector(args.marker)),
        keycloakPage: Boolean(document.querySelector(args.keycloakPage)),
      }),
      { marker, keycloakPage: PROBE_SELECTORS.keycloakPage },
    );
    if (state.keycloakPage) {
      throw new Error(`${step}: Keycloak rendered a page`);
    }
    if (state.path === wantPath && state.ready) {
      return;
    }
    if (
      state.path !== wantPath &&
      state.path.startsWith("/postflight") &&
      !state.path.startsWith("/postflight/auth/")
    ) {
      throw new Error(
        `${step}: landed on ${JSON.stringify(state.path)}, want ${JSON.stringify(wantPath)}`,
      );
    }
    await page.waitForTimeout(250);
  }
  throw new Error(`${step}: ${JSON.stringify(wantPath)} did not render`);
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

  step("github-login");
  await page.locator(SELECTORS.githubLogin).fill(cfg.githubUsername);
  await page.locator(SELECTORS.githubPassword).fill(cfg.githubPassword);
  await page.locator(SELECTORS.githubSubmit).first().click();
  step("github-authorization");
  await finishGitHubAuthorization(page, cfg);

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

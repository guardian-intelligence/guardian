import { expect, type Page } from "@playwright/test";
import { classifyOAuthPage } from "./classify.ts";
import type { JourneyConfig } from "./config.ts";
import { PROBE_SELECTORS, SELECTORS, oauthPageProbe } from "./probes.ts";
import { totp, totpBoundaryDelayMs } from "./totp.ts";

// Step markers reach the pod log through the reporter's stdout forwarding;
// a hung step is then named by the last marker emitted.
export function step(name: string): void {
  process.stdout.write(`${JSON.stringify({ event: "step", name })}\n`);
}

export async function awaitGitHubRedirect(page: Page): Promise<void> {
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

// GitHub rejects a TOTP code it has already accepted, and a single canary run
// now signs in more than once. All journeys share one worker process, so a
// module-level record of the last consumed 30-second window is enough to
// force each login onto a fresh code. That is a single-account constraint,
// not an orchestration preference: parallel journeys need per-account
// leases (the canary orchestrator design), so refuse extra workers loudly
// instead of letting cross-process TOTP reuse surface as flaky failures.
let lastTotpWindow = -1;

async function nextTotpCode(page: Page, seed: string): Promise<string> {
  // TEST_PARALLEL_INDEX is the concurrency slot; TEST_WORKER_INDEX would
  // misfire here because it increments every time Playwright replaces a
  // worker after an unrelated failure.
  if (process.env.TEST_PARALLEL_INDEX !== undefined && process.env.TEST_PARALLEL_INDEX !== "0") {
    throw new Error(
      "GitHub-account journeys serialize on one TOTP account and must run in a single worker; " +
        "parallel workers require per-account leases from the canary orchestrator",
    );
  }
  const windowOf = (ms: number): number => Math.floor(ms / 30_000);
  const now = Date.now();
  if (windowOf(now) === lastTotpWindow) {
    await page.waitForTimeout(30_000 - (now % 30_000) + 500);
  }
  const delay = totpBoundaryDelayMs(new Date());
  if (delay > 0) {
    await page.waitForTimeout(delay);
  }
  lastTotpWindow = windowOf(Date.now());
  return totp(seed, new Date());
}

export async function finishGitHubAuthorization(page: Page, cfg: JourneyConfig): Promise<void> {
  const deadline = Date.now() + 105_000;
  let totpSent = false;
  let grantSent = false;
  while (Date.now() < deadline) {
    let state;
    try {
      state = await page.evaluate(oauthPageProbe, PROBE_SELECTORS);
    } catch {
      // A redirect (the device flow ends in a meta-refresh bounce)
      // destroyed the execution context mid-probe; poll again.
      await page.waitForTimeout(250);
      continue;
    }
    const action = classifyOAuthPage(state, cfg.guardianHost);
    switch (action) {
      case "complete":
        return;
      case "submit-totp": {
        if (totpSent) {
          await page.waitForTimeout(250);
          break;
        }
        const code = await nextTotpCode(page, cfg.githubTotpSeed);
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

export async function signInAtGitHub(page: Page, cfg: JourneyConfig): Promise<void> {
  await page.locator(SELECTORS.githubLogin).fill(cfg.githubUsername);
  await page.locator(SELECTORS.githubPassword).fill(cfg.githubPassword);
  await page.locator(SELECTORS.githubSubmit).first().click();
  await finishGitHubAuthorization(page, cfg);
}

export async function awaitPostflightLanding(
  page: Page,
  wantPath: string,
  marker: string,
  stepName: string,
): Promise<void> {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    let state;
    try {
      state = await page.evaluate(
        (args: { marker: string; keycloakPage: string }) => ({
          path: location.pathname,
          ready: Boolean(document.querySelector(args.marker)),
          keycloakPage: Boolean(document.querySelector(args.keycloakPage)),
        }),
        { marker, keycloakPage: PROBE_SELECTORS.keycloakPage },
      );
    } catch {
      // A meta-refresh or redirect destroyed the execution context
      // mid-probe; the next poll sees the settled document.
      await page.waitForTimeout(250);
      continue;
    }
    if (state.keycloakPage) {
      throw new Error(`${stepName}: Keycloak rendered a page`);
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
        `${stepName}: landed on ${JSON.stringify(state.path)}, want ${JSON.stringify(wantPath)}`,
      );
    }
    await page.waitForTimeout(250);
  }
  throw new Error(`${stepName}: ${JSON.stringify(wantPath)} did not render`);
}

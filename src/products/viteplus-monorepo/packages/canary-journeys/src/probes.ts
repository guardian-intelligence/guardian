import type { OAuthPageState } from "./classify.ts";

export const SELECTORS = {
  signIn: "#postflight-sign-in",
  githubLogin: "input[name=login]",
  githubPassword: "input[name=password]",
  githubSubmit: "input[type=submit], button[type=submit]",
  totpInput: "input[name=otp], input[name=app_otp], input#app_totp",
  grantEnabled:
    "button[name=authorize][value='1']:not([disabled]), input[name=authorize][value='1']:not([disabled]), button[value=authorize]:not([disabled])",
  consoleReady: "[data-postflight-console=ready]",
  oobeReady: "[data-postflight-oobe=ready]",
} as const;

export interface ProbeSelectors {
  totpInput: string;
  grantEnabled: string;
  grantBlocked: string;
  keycloakPage: string;
  collision: string;
  errors: string;
}

// A rendered Keycloak document. Redirect pass-throughs under /realms/ are
// legitimate and carry none of this DOM, so the probe never keys on the URL.
export const PROBE_SELECTORS: ProbeSelectors = {
  totpInput: SELECTORS.totpInput,
  grantEnabled: SELECTORS.grantEnabled,
  grantBlocked:
    "button[name=authorize][value='1'][disabled], input[name=authorize][value='1'][disabled], button[value=authorize][disabled]",
  keycloakPage:
    "#kc-page-title, #kc-header, .login-pf-page, #kc-error-message, form#kc-idp-review-profile-form",
  collision: "#linkAccount, #instruction1",
  errors: ".flash-error, [data-test-selector=auth-error], #kc-error-message, .pf-m-danger",
};

// Runs inside the browser via page.evaluate: it must stay self-contained,
// reading only its argument and DOM globals.
export function oauthPageProbe(sel: ProbeSelectors): OAuthPageState {
  const visible = (element: Element): boolean => {
    const style = getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    return (
      style.display !== "none" && style.visibility !== "hidden" && rect.width > 0 && rect.height > 0
    );
  };
  return {
    host: location.hostname,
    path: location.pathname,
    hasTOTP: Boolean(document.querySelector(sel.totpInput)),
    canGrant: Boolean(document.querySelector(sel.grantEnabled)),
    grantBlocked: Boolean(document.querySelector(sel.grantBlocked)),
    hasKeycloakPage: Boolean(document.querySelector(sel.keycloakPage)),
    hasCollision: Boolean(document.querySelector(sel.collision)),
    hasError: Array.from(document.querySelectorAll(sel.errors)).some(visible),
  };
}

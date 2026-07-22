export interface OAuthPageState {
  host: string;
  path: string;
  hasTOTP: boolean;
  canGrant: boolean;
  grantBlocked: boolean;
  hasKeycloakPage: boolean;
  hasCollision: boolean;
  hasError: boolean;
}

export type OAuthPageAction = "wait" | "complete" | "submit-totp" | "grant";

export function classifyOAuthPage(state: OAuthPageState, guardianHost: string): OAuthPageAction {
  if (state.host === guardianHost) {
    if (state.path.startsWith("/postflight")) {
      return "complete";
    }
    if (state.hasCollision) {
      throw new Error("Guardian refused automatic linking for an existing account");
    }
    if (state.hasError) {
      throw new Error("Guardian rejected the brokered login");
    }
    if (state.hasKeycloakPage) {
      throw new Error("Keycloak rendered a page during the brokered login");
    }
    return "wait";
  }
  if (state.host === "github.com") {
    if (state.hasError) {
      throw new Error("GitHub rejected the canary login");
    }
    if (state.grantBlocked) {
      throw new Error(
        "GitHub OAuth authorization is disabled; verify account readiness, the persisted app grant, and OAuth token cadence",
      );
    }
    if (state.hasTOTP) {
      return "submit-totp";
    }
    if (state.canGrant) {
      return "grant";
    }
    return "wait";
  }
  throw new Error(`OAuth flow reached unexpected host ${JSON.stringify(state.host)}`);
}

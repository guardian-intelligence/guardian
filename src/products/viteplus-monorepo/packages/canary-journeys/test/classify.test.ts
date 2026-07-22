import { describe, expect, it } from "vitest";
import { classifyOAuthPage, type OAuthPageState } from "../src/classify.ts";

const GUARDIAN_HOST = "guardianintelligence.org";

function state(partial: Partial<OAuthPageState>): OAuthPageState {
  return {
    host: "",
    path: "",
    hasTOTP: false,
    canGrant: false,
    grantBlocked: false,
    hasKeycloakPage: false,
    hasCollision: false,
    hasError: false,
    ...partial,
  };
}

describe("classifyOAuthPage", () => {
  it("completes on an already-linked return", () => {
    expect(
      classifyOAuthPage(
        state({ host: GUARDIAN_HOST, path: "/postflight/auth/callback" }),
        GUARDIAN_HOST,
      ),
    ).toBe("complete");
  });

  it("waits through broker redirect pass-throughs", () => {
    expect(
      classifyOAuthPage(
        state({
          host: GUARDIAN_HOST,
          path: "/realms/guardianintelligence.org/broker/github/endpoint",
        }),
        GUARDIAN_HOST,
      ),
    ).toBe("wait");
  });

  it("fails when Keycloak renders a page", () => {
    expect(() =>
      classifyOAuthPage(
        state({
          host: GUARDIAN_HOST,
          path: "/realms/guardianintelligence.org/login-actions/first-broker-login",
          hasKeycloakPage: true,
        }),
        GUARDIAN_HOST,
      ),
    ).toThrow(/Keycloak rendered a page/);
  });

  it("never auto-links an email collision", () => {
    expect(() =>
      classifyOAuthPage(
        state({
          host: GUARDIAN_HOST,
          path: "/realms/guardianintelligence.org/login-actions/first-broker-login",
          hasCollision: true,
        }),
        GUARDIAN_HOST,
      ),
    ).toThrow(/refused automatic linking/);
  });

  it("submits TOTP on the two-factor page", () => {
    expect(
      classifyOAuthPage(
        state({ host: "github.com", path: "/sessions/two-factor/app", hasTOTP: true }),
        GUARDIAN_HOST,
      ),
    ).toBe("submit-totp");
  });

  it("grants on the consent page", () => {
    expect(
      classifyOAuthPage(
        state({ host: "github.com", path: "/login/oauth/authorize", canGrant: true }),
        GUARDIAN_HOST,
      ),
    ).toBe("grant");
  });

  it("fails on a disabled consent button", () => {
    expect(() =>
      classifyOAuthPage(
        state({
          host: "github.com",
          path: "/login/oauth/authorize",
          grantBlocked: true,
        }),
        GUARDIAN_HOST,
      ),
    ).toThrow(/persisted app grant/);
  });

  it("fails on a visible GitHub error", () => {
    expect(() =>
      classifyOAuthPage(
        state({ host: "github.com", path: "/login", hasError: true }),
        GUARDIAN_HOST,
      ),
    ).toThrow(/rejected the canary login/);
  });

  it("waits on GitHub redirects without consent", () => {
    expect(
      classifyOAuthPage(
        state({ host: "github.com", path: "/login/oauth/authorize" }),
        GUARDIAN_HOST,
      ),
    ).toBe("wait");
  });

  it("fails on an unknown origin", () => {
    expect(() =>
      classifyOAuthPage(state({ host: "example.com", path: "/login" }), GUARDIAN_HOST),
    ).toThrow(/unexpected host/);
  });
});

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  AUTH_TRANSACTION_COOKIE,
  SESSION_COOKIE,
  beginPostflightLogin,
  completePostflightLogin,
  deviceContinueRedirect,
  validUserCode,
  endPostflightSession,
  postflightSessionResponse,
} from "./postflight-auth";

const publicURL = "https://guardianintelligence.org";
const issuer = `${publicURL}/realms/guardianintelligence.org`;

function cookieFrom(response: Response, name: string): string {
  const match = response.headers.get("set-cookie")?.match(new RegExp(`${name}=([^;,]+)`));
  if (!match?.[1]) throw new Error(`${name} was not set`);
  return match[1];
}

function encodeJSON(value: unknown): string {
  return Buffer.from(JSON.stringify(value)).toString("base64url");
}

describe("Postflight OIDC BFF", () => {
  beforeEach(() => {
    process.env.POSTFLIGHT_PUBLIC_URL = publicURL;
    process.env.POSTFLIGHT_OIDC_ISSUER = issuer;
    process.env.POSTFLIGHT_OIDC_INTERNAL_URL =
      "http://keycloak.test/realms/guardianintelligence.org";
    process.env.POSTFLIGHT_OIDC_CLIENT_ID = "postflight-web";
    process.env.POSTFLIGHT_OIDC_CLIENT_SECRET = "client-secret";
    process.env.POSTFLIGHT_SESSION_SECRET = "s".repeat(64);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    for (const name of [
      "POSTFLIGHT_PUBLIC_URL",
      "POSTFLIGHT_OIDC_ISSUER",
      "POSTFLIGHT_OIDC_INTERNAL_URL",
      "POSTFLIGHT_OIDC_CLIENT_ID",
      "POSTFLIGHT_OIDC_CLIENT_SECRET",
      "POSTFLIGHT_SESSION_SECRET",
    ]) {
      delete process.env[name];
    }
  });

  it("uses state, nonce, PKCE S256, a pinned GitHub broker, and an encrypted host-only transaction", async () => {
    const response = await beginPostflightLogin(
      new Request(`${publicURL}/postflight/auth/login?return_to=/postflight?from=canary`),
    );
    const location = new URL(response.headers.get("location") || "");

    expect(response.status).toBe(303);
    expect(location.origin + location.pathname).toBe(`${issuer}/protocol/openid-connect/auth`);
    expect(location.searchParams.get("response_type")).toBe("code");
    expect(location.searchParams.get("code_challenge_method")).toBe("S256");
    expect(location.searchParams.get("code_challenge")).toHaveLength(43);
    expect(location.searchParams.get("state")).toHaveLength(43);
    expect(location.searchParams.get("nonce")).toHaveLength(43);
    expect(location.searchParams.get("kc_idp_hint")).toBe("github");
    expect(response.headers.get("set-cookie")).toContain(`${AUTH_TRANSACTION_COOKIE}=`);
    expect(response.headers.get("set-cookie")).toContain("HttpOnly; Secure; SameSite=Lax");
  });

  it("fails closed on an invalid transaction and clears it", async () => {
    const transactionResponse = await beginPostflightLogin(
      new Request(`${publicURL}/postflight/auth/login`),
    );
    const transaction = cookieFrom(transactionResponse, AUTH_TRANSACTION_COOKIE);

    const response = await completePostflightLogin(
      new Request(`${publicURL}/postflight/auth/callback?code=code&state=wrong`, {
        headers: { cookie: `${AUTH_TRANSACTION_COOKIE}=${transaction}` },
      }),
    );

    expect(response.status).toBe(303);
    expect(response.headers.get("location")).toBe(
      `${publicURL}/postflight?auth_error=transaction_invalid`,
    );
    expect(response.headers.get("set-cookie")).toContain(`${AUTH_TRANSACTION_COOKIE}=`);
    expect(response.headers.get("set-cookie")).toContain("Max-Age=0");
  });

  it("does not accept a forged local session", async () => {
    const response = await postflightSessionResponse(
      new Request(`${publicURL}/postflight/auth/session`, {
        headers: { cookie: `${SESSION_COOKIE}=not-a-sealed-session` },
      }),
    );

    expect(response.status).toBe(401);
    expect(await response.json()).toEqual({ authenticated: false });
  });

  it("exchanges and verifies the code, seals a local session, and supplies an ID token logout hint", async () => {
    const transactionResponse = await beginPostflightLogin(
      new Request(`${publicURL}/postflight/auth/login`),
    );
    const authorizationURL = new URL(transactionResponse.headers.get("location") || "");
    const transaction = cookieFrom(transactionResponse, AUTH_TRANSACTION_COOKIE);
    const nonce = authorizationURL.searchParams.get("nonce");
    const state = authorizationURL.searchParams.get("state");

    const keyPair = (await crypto.subtle.generateKey(
      {
        name: "RSASSA-PKCS1-v1_5",
        modulusLength: 2048,
        publicExponent: new Uint8Array([1, 0, 1]),
        hash: "SHA-256",
      },
      true,
      ["sign", "verify"],
    )) as CryptoKeyPair;
    const publicKey = await crypto.subtle.exportKey("jwk", keyPair.publicKey);
    const header = encodeJSON({ alg: "RS256", kid: "test-key", typ: "JWT" });
    const claims = encodeJSON({
      iss: issuer,
      sub: "guardian-subject",
      aud: "postflight-web",
      exp: Math.floor(Date.now() / 1000) + 300,
      iat: Math.floor(Date.now() / 1000),
      nonce,
      preferred_username: "canary",
      email: "untrusted@example.com",
      email_verified: false,
    });
    const signingInput = `${header}.${claims}`;
    const signature = await crypto.subtle.sign(
      "RSASSA-PKCS1-v1_5",
      keyPair.privateKey,
      new TextEncoder().encode(signingInput),
    );
    const idToken = `${signingInput}.${Buffer.from(signature).toString("base64url")}`;

    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/protocol/openid-connect/token")) {
        expect(init?.method).toBe("POST");
        expect(String(init?.body)).toContain("code_verifier=");
        expect(new Headers(init?.headers).get("authorization")).toMatch(/^Basic /);
        return Response.json({ id_token: idToken });
      }
      if (url.endsWith("/protocol/openid-connect/certs")) {
        return Response.json({ keys: [{ ...publicKey, kid: "test-key", alg: "RS256" }] });
      }
      throw new Error(`unexpected fetch ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const callbackResponse = await completePostflightLogin(
      new Request(`${publicURL}/postflight/auth/callback?code=authorization-code&state=${state}`, {
        headers: { cookie: `${AUTH_TRANSACTION_COOKIE}=${transaction}` },
      }),
    );
    expect(callbackResponse.status).toBe(303);
    expect(callbackResponse.headers.get("location")).toBe("/postflight/console");
    const session = cookieFrom(callbackResponse, SESSION_COOKIE);

    const sessionResponse = await postflightSessionResponse(
      new Request(`${publicURL}/postflight/auth/session`, {
        headers: { cookie: `${SESSION_COOKIE}=${session}` },
      }),
    );
    expect(sessionResponse.status).toBe(200);
    const body = await sessionResponse.json();
    expect(body).toMatchObject({
      authenticated: true,
      user: { subject: "guardian-subject", username: "canary" },
    });
    expect(body.user).not.toHaveProperty("email");
    expect(JSON.stringify(body)).not.toContain(idToken);

    const logoutResponse = await endPostflightSession(
      new Request(`${publicURL}/postflight/auth/logout`, {
        headers: { cookie: `${SESSION_COOKIE}=${session}` },
      }),
    );
    const logoutURL = new URL(logoutResponse.headers.get("location") || "");
    expect(logoutResponse.status).toBe(303);
    expect(logoutURL.origin + logoutURL.pathname).toBe(`${issuer}/protocol/openid-connect/logout`);
    expect(logoutURL.searchParams.get("id_token_hint")).toBe(idToken);
    expect(logoutURL.searchParams.get("client_id")).toBe("postflight-web");
    expect(logoutURL.searchParams.get("post_logout_redirect_uri")).toBe(`${publicURL}/postflight`);
    expect(logoutResponse.headers.get("set-cookie")).toContain(`${SESSION_COOKIE}=`);
    expect(logoutResponse.headers.get("set-cookie")).toContain("Max-Age=0");
  });

  it("refuses a cross-site logout trigger", async () => {
    const response = await endPostflightSession(
      new Request(`${publicURL}/postflight/auth/logout`, {
        headers: { "sec-fetch-site": "cross-site" },
      }),
    );

    expect(response.status).toBe(403);
    expect(response.headers.get("set-cookie")).toBeNull();
  });

  it("refuses a cross-origin logout navigation from a browser without Fetch Metadata", async () => {
    const response = await endPostflightSession(
      new Request(`${publicURL}/postflight/auth/logout`, {
        headers: { referer: "https://evil.example/logout-trap" },
      }),
    );

    expect(response.status).toBe(403);
    expect(response.headers.get("set-cookie")).toBeNull();
  });

  it("accepts a same-origin logout navigation from a browser without Fetch Metadata", async () => {
    const response = await endPostflightSession(
      new Request(`${publicURL}/postflight/auth/logout`, {
        headers: { referer: `${publicURL}/postflight/console` },
      }),
    );

    expect(response.status).toBe(303);
    expect(response.headers.get("location")).toBe(`${publicURL}/postflight`);
  });

  it("signs out locally without visiting Keycloak when no ID token is recoverable", async () => {
    const response = await endPostflightSession(
      new Request(`${publicURL}/postflight/auth/logout`, {
        headers: { cookie: `${SESSION_COOKIE}=not-a-sealed-session` },
      }),
    );

    expect(response.status).toBe(303);
    expect(response.headers.get("location")).toBe(`${publicURL}/postflight`);
    expect(response.headers.get("set-cookie")).toContain(`${SESSION_COOKIE}=`);
    expect(response.headers.get("set-cookie")).toContain("Max-Age=0");
  });
});

describe("device continue redirect", () => {
  beforeEach(() => {
    process.env.POSTFLIGHT_PUBLIC_URL = publicURL;
    process.env.POSTFLIGHT_OIDC_ISSUER = issuer;
    process.env.POSTFLIGHT_OIDC_CLIENT_SECRET = "client-secret";
    process.env.POSTFLIGHT_SESSION_SECRET = "s".repeat(64);
  });

  afterEach(() => {
    for (const name of [
      "POSTFLIGHT_PUBLIC_URL",
      "POSTFLIGHT_OIDC_ISSUER",
      "POSTFLIGHT_OIDC_CLIENT_SECRET",
      "POSTFLIGHT_SESSION_SECRET",
    ]) {
      delete process.env[name];
    }
  });

  it("forwards to the issuer's device verification with the code attached", () => {
    const response = deviceContinueRedirect(
      new Request(`${publicURL}/postflight/device/continue?user_code=wdjb-mjht`),
    );

    expect(response.status).toBe(303);
    expect(response.headers.get("location")).toBe(`${issuer}/device?user_code=WDJB-MJHT`);
    expect(response.headers.get("cache-control")).toBe("no-store");
  });

  it("bounces back to the approval page when the code is malformed", () => {
    const response = deviceContinueRedirect(
      new Request(
        `${publicURL}/postflight/device/continue?user_code=${encodeURIComponent("<script>x")}`,
      ),
    );

    expect(response.status).toBe(303);
    expect(response.headers.get("location")).toBe(`${publicURL}/postflight/device`);
  });

  it("bounces back to the approval page when no code is supplied", () => {
    const response = deviceContinueRedirect(new Request(`${publicURL}/postflight/device/continue`));

    expect(response.status).toBe(303);
    expect(response.headers.get("location")).toBe(`${publicURL}/postflight/device`);
  });
});

describe("validUserCode", () => {
  it("normalizes well-formed codes to uppercase", () => {
    expect(validUserCode("wdjb-mjht")).toBe("WDJB-MJHT");
  });

  it("rejects malformed and missing codes", () => {
    expect(validUserCode("<script>x")).toBeNull();
    expect(validUserCode("abc")).toBeNull();
    expect(validUserCode(null)).toBeNull();
    expect(validUserCode(undefined)).toBeNull();
  });
});

describe("session seal keyring", () => {
  // A test-local copy of the sealed-cookie wire format: rotation only works
  // if sessions minted before a deploy still unseal after it, so the format
  // itself is load-bearing.
  async function sealSession(session: unknown, secret: string): Promise<string> {
    const purpose = "postflight:session:v1";
    const raw = await crypto.subtle.digest(
      "SHA-256",
      new TextEncoder().encode(`${purpose}\0${secret}`),
    );
    const key = await crypto.subtle.importKey("raw", raw, { name: "AES-GCM" }, false, ["encrypt"]);
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const ciphertext = await crypto.subtle.encrypt(
      { name: "AES-GCM", iv, additionalData: new TextEncoder().encode(purpose) },
      key,
      new TextEncoder().encode(JSON.stringify(session)),
    );
    const output = new Uint8Array(iv.length + ciphertext.byteLength);
    output.set(iv);
    output.set(new Uint8Array(ciphertext), iv.length);
    return Buffer.from(output).toString("base64url");
  }

  const session = {
    subject: "guardian-subject",
    username: "canary",
    expiresAt: Date.now() + 10 * 60 * 1000,
    idToken: "id-token",
  };

  beforeEach(() => {
    process.env.POSTFLIGHT_PUBLIC_URL = publicURL;
    process.env.POSTFLIGHT_OIDC_ISSUER = issuer;
    process.env.POSTFLIGHT_OIDC_CLIENT_SECRET = "client-secret";
  });

  afterEach(() => {
    for (const name of [
      "POSTFLIGHT_PUBLIC_URL",
      "POSTFLIGHT_OIDC_ISSUER",
      "POSTFLIGHT_OIDC_CLIENT_SECRET",
      "POSTFLIGHT_SESSION_SECRET",
      "POSTFLIGHT_SESSION_SECRET_STANDBY",
      "POSTFLIGHT_SESSION_SECRET_FILE",
    ]) {
      delete process.env[name];
    }
  });

  it("accepts a session sealed with the standby secret after a rotation flip", async () => {
    const retiring = "a".repeat(64);
    process.env.POSTFLIGHT_SESSION_SECRET = "b".repeat(64);
    process.env.POSTFLIGHT_SESSION_SECRET_STANDBY = retiring;

    const response = await postflightSessionResponse(
      new Request(`${publicURL}/postflight/auth/session`, {
        headers: { cookie: `${SESSION_COOKIE}=${await sealSession(session, retiring)}` },
      }),
    );

    expect(response.status).toBe(200);
    expect(await response.json()).toMatchObject({
      authenticated: true,
      user: { subject: "guardian-subject" },
    });
  });

  it("rejects a session sealed with a secret no slot holds", async () => {
    process.env.POSTFLIGHT_SESSION_SECRET = "b".repeat(64);
    process.env.POSTFLIGHT_SESSION_SECRET_STANDBY = "c".repeat(64);

    const response = await postflightSessionResponse(
      new Request(`${publicURL}/postflight/auth/session`, {
        headers: { cookie: `${SESSION_COOKIE}=${await sealSession(session, "a".repeat(64))}` },
      }),
    );

    expect(response.status).toBe(401);
  });

  it("re-reads a *_FILE secret on every use", async () => {
    const { mkdtempSync, writeFileSync } = await import("node:fs");
    const { tmpdir } = await import("node:os");
    const { join } = await import("node:path");
    const dir = mkdtempSync(join(tmpdir(), "seal-"));
    const sealPath = join(dir, "session-seal");
    writeFileSync(sealPath, `${"a".repeat(64)}\n`);
    process.env.POSTFLIGHT_SESSION_SECRET_FILE = sealPath;

    const sealedWithFirst = await sealSession(session, "a".repeat(64));
    const before = await postflightSessionResponse(
      new Request(`${publicURL}/postflight/auth/session`, {
        headers: { cookie: `${SESSION_COOKIE}=${sealedWithFirst}` },
      }),
    );
    expect(before.status).toBe(200);

    writeFileSync(sealPath, `${"b".repeat(64)}\n`);
    const after = await postflightSessionResponse(
      new Request(`${publicURL}/postflight/auth/session`, {
        headers: { cookie: `${SESSION_COOKIE}=${sealedWithFirst}` },
      }),
    );
    expect(after.status).toBe(401);

    const reSealed = await postflightSessionResponse(
      new Request(`${publicURL}/postflight/auth/session`, {
        headers: { cookie: `${SESSION_COOKIE}=${await sealSession(session, "b".repeat(64))}` },
      }),
    );
    expect(reSealed.status).toBe(200);
  });
});

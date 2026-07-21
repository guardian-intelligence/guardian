const encoder = new TextEncoder();
const decoder = new TextDecoder();

export const AUTH_TRANSACTION_COOKIE = "__Host-postflight-auth";
export const SESSION_COOKIE = "__Host-postflight-session";

const transactionPurpose = "postflight:oidc-transaction:v1";
const sessionPurpose = "postflight:session:v1";

// Server-side constant, never derived from request input: pinning the realm's
// single broker keeps Keycloak from rendering its own login page.
const identityProviderHint = "github";

type AuthTransaction = {
  readonly state: string;
  readonly nonce: string;
  readonly verifier: string;
  readonly returnTo: string;
  readonly expiresAt: number;
};

export type PostflightSession = {
  readonly subject: string;
  readonly username: string;
  readonly email?: string;
  readonly name?: string;
  readonly expiresAt: number;
};

type SealedPostflightSession = PostflightSession & {
  readonly idToken: string;
};

type IDTokenClaims = {
  readonly iss: string;
  readonly sub: string;
  readonly aud: string | readonly string[];
  readonly azp?: string;
  readonly exp: number;
  readonly iat?: number;
  readonly nonce: string;
  readonly preferred_username?: string;
  readonly email?: string;
  readonly email_verified?: boolean;
  readonly name?: string;
};

type JSONWebKeySet = {
  readonly keys: readonly (JsonWebKey & { readonly kid?: string })[];
};

type TokenResponse = {
  readonly id_token?: string;
};

function env(name: string, fallback?: string): string {
  const value = process.env[name]?.trim() || fallback;
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function configuration() {
  const publicURL = env("POSTFLIGHT_PUBLIC_URL", "https://guardianintelligence.org").replace(
    /\/$/,
    "",
  );
  const issuer = env(
    "POSTFLIGHT_OIDC_ISSUER",
    `${publicURL}/realms/guardianintelligence.org`,
  ).replace(/\/$/, "");
  return {
    publicURL,
    issuer,
    internalIssuer: env("POSTFLIGHT_OIDC_INTERNAL_URL", issuer).replace(/\/$/, ""),
    clientID: env("POSTFLIGHT_OIDC_CLIENT_ID", "postflight-web"),
    clientSecret: env("POSTFLIGHT_OIDC_CLIENT_SECRET"),
    sessionSecret: env("POSTFLIGHT_SESSION_SECRET"),
    callbackURL: `${publicURL}/postflight/auth/callback`,
  };
}

function base64URL(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString("base64url");
}

function decodeBase64URL(value: string): Uint8Array {
  return new Uint8Array(Buffer.from(value, "base64url"));
}

function randomValue(bytes = 32): string {
  return base64URL(crypto.getRandomValues(new Uint8Array(bytes)));
}

async function digest(value: string): Promise<Uint8Array> {
  return new Uint8Array(await crypto.subtle.digest("SHA-256", encoder.encode(value)));
}

async function sealingKey(secret: string, purpose: string): Promise<CryptoKey> {
  if (secret.length < 32)
    throw new Error("POSTFLIGHT_SESSION_SECRET must be at least 32 characters");
  const raw = await digest(`${purpose}\0${secret}`);
  return crypto.subtle.importKey("raw", raw.buffer as ArrayBuffer, { name: "AES-GCM" }, false, [
    "encrypt",
    "decrypt",
  ]);
}

async function seal(value: unknown, purpose: string, secret: string): Promise<string> {
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const ciphertext = await crypto.subtle.encrypt(
    { name: "AES-GCM", iv, additionalData: encoder.encode(purpose) },
    await sealingKey(secret, purpose),
    encoder.encode(JSON.stringify(value)),
  );
  const output = new Uint8Array(iv.length + ciphertext.byteLength);
  output.set(iv);
  output.set(new Uint8Array(ciphertext), iv.length);
  return base64URL(output);
}

async function unseal<T>(value: string, purpose: string, secret: string): Promise<T> {
  const input = decodeBase64URL(value);
  if (input.length < 29) throw new Error("sealed value is malformed");
  const plaintext = await crypto.subtle.decrypt(
    {
      name: "AES-GCM",
      iv: input.slice(0, 12),
      additionalData: encoder.encode(purpose),
    },
    await sealingKey(secret, purpose),
    input.slice(12),
  );
  return JSON.parse(decoder.decode(plaintext)) as T;
}

function cookieValue(request: Request, name: string): string | undefined {
  for (const part of (request.headers.get("cookie") || "").split(";")) {
    const [key, ...value] = part.trim().split("=");
    if (key === name) return value.join("=");
  }
  return undefined;
}

function cookie(name: string, value: string, maxAge: number): string {
  return `${name}=${value}; Path=/; Max-Age=${maxAge}; HttpOnly; Secure; SameSite=Lax`;
}

function clearCookie(name: string): string {
  return cookie(name, "", 0);
}

function safeReturnTo(value: string | null): string {
  if (
    !value ||
    value.startsWith("//") ||
    !(
      value === "/postflight" ||
      value.startsWith("/postflight/") ||
      value.startsWith("/postflight?")
    )
  ) {
    return "/postflight/console";
  }
  return value;
}

function securityHeaders(): HeadersInit {
  return {
    "cache-control": "no-store",
    "content-security-policy": "default-src 'none'; frame-ancestors 'none'; base-uri 'none'",
    "referrer-policy": "no-referrer",
    "x-content-type-options": "nosniff",
  };
}

function errorRedirect(publicURL: string, code: string): Response {
  return new Response(null, {
    status: 303,
    headers: {
      ...securityHeaders(),
      location: `${publicURL}/postflight?auth_error=${encodeURIComponent(code)}`,
      "set-cookie": clearCookie(AUTH_TRANSACTION_COOKIE),
    },
  });
}

export async function beginPostflightLogin(request: Request): Promise<Response> {
  const cfg = configuration();
  const requestURL = new URL(request.url);
  const verifier = randomValue(48);
  const transaction: AuthTransaction = {
    state: randomValue(),
    nonce: randomValue(),
    verifier,
    returnTo: safeReturnTo(requestURL.searchParams.get("return_to")),
    expiresAt: Date.now() + 10 * 60 * 1000,
  };
  const authorizationURL = new URL(`${cfg.issuer}/protocol/openid-connect/auth`);
  authorizationURL.search = new URLSearchParams({
    client_id: cfg.clientID,
    redirect_uri: cfg.callbackURL,
    response_type: "code",
    scope: "openid profile email",
    state: transaction.state,
    nonce: transaction.nonce,
    code_challenge: base64URL(await digest(verifier)),
    code_challenge_method: "S256",
    kc_idp_hint: identityProviderHint,
  }).toString();

  const sealed = await seal(transaction, transactionPurpose, cfg.sessionSecret);
  return new Response(null, {
    status: 303,
    headers: {
      ...securityHeaders(),
      location: authorizationURL.toString(),
      "set-cookie": cookie(AUTH_TRANSACTION_COOKIE, sealed, 10 * 60),
    },
  });
}

function parseJWT(token: string): {
  readonly signingInput: Uint8Array;
  readonly signature: Uint8Array;
  readonly header: { readonly alg?: string; readonly kid?: string };
  readonly claims: IDTokenClaims;
} {
  const parts = token.split(".");
  if (parts.length !== 3) throw new Error("ID token is malformed");
  const [encodedHeader, encodedPayload, encodedSignature] = parts as [string, string, string];
  return {
    signingInput: encoder.encode(`${encodedHeader}.${encodedPayload}`),
    signature: decodeBase64URL(encodedSignature),
    header: JSON.parse(decoder.decode(decodeBase64URL(encodedHeader))),
    claims: JSON.parse(decoder.decode(decodeBase64URL(encodedPayload))),
  };
}

async function validateIDToken(
  token: string,
  expectedNonce: string,
  cfg: ReturnType<typeof configuration>,
): Promise<IDTokenClaims> {
  const parsed = parseJWT(token);
  if (parsed.header.alg !== "RS256" || !parsed.header.kid) {
    throw new Error("ID token algorithm is not allowed");
  }
  const response = await fetch(`${cfg.internalIssuer}/protocol/openid-connect/certs`, {
    headers: { accept: "application/json" },
    signal: AbortSignal.timeout(10_000),
  });
  if (!response.ok) throw new Error(`JWKS returned HTTP ${response.status}`);
  const jwks = (await response.json()) as JSONWebKeySet;
  const jwk = jwks.keys.find((candidate) => candidate.kid === parsed.header.kid);
  if (!jwk) throw new Error("ID token key is unknown");
  const key = await crypto.subtle.importKey(
    "jwk",
    jwk,
    { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" },
    false,
    ["verify"],
  );
  const valid = await crypto.subtle.verify(
    "RSASSA-PKCS1-v1_5",
    key,
    parsed.signature.buffer as ArrayBuffer,
    parsed.signingInput.buffer as ArrayBuffer,
  );
  if (!valid) throw new Error("ID token signature is invalid");

  const now = Math.floor(Date.now() / 1000);
  const audiences = Array.isArray(parsed.claims.aud) ? parsed.claims.aud : [parsed.claims.aud];
  if (
    typeof parsed.claims.iss !== "string" ||
    typeof parsed.claims.sub !== "string" ||
    typeof parsed.claims.exp !== "number" ||
    typeof parsed.claims.nonce !== "string" ||
    parsed.claims.iss !== cfg.issuer ||
    !audiences.includes(cfg.clientID) ||
    (audiences.length > 1 && parsed.claims.azp !== cfg.clientID) ||
    parsed.claims.exp <= now ||
    (parsed.claims.iat !== undefined && parsed.claims.iat > now + 60) ||
    parsed.claims.nonce !== expectedNonce ||
    !parsed.claims.sub
  ) {
    throw new Error("ID token claims are invalid");
  }
  return parsed.claims;
}

export async function completePostflightLogin(request: Request): Promise<Response> {
  const cfg = configuration();
  const callback = new URL(request.url);
  if (callback.searchParams.has("error")) {
    return errorRedirect(cfg.publicURL, "provider_cancelled");
  }
  const sealedTransaction = cookieValue(request, AUTH_TRANSACTION_COOKIE);
  if (!sealedTransaction) return errorRedirect(cfg.publicURL, "transaction_missing");

  let transaction: AuthTransaction;
  try {
    transaction = await unseal<AuthTransaction>(
      sealedTransaction,
      transactionPurpose,
      cfg.sessionSecret,
    );
  } catch {
    return errorRedirect(cfg.publicURL, "transaction_invalid");
  }
  if (
    transaction.expiresAt <= Date.now() ||
    callback.searchParams.get("state") !== transaction.state
  ) {
    return errorRedirect(cfg.publicURL, "transaction_invalid");
  }
  const code = callback.searchParams.get("code");
  if (!code) return errorRedirect(cfg.publicURL, "code_missing");

  const tokenResponse = await fetch(`${cfg.internalIssuer}/protocol/openid-connect/token`, {
    method: "POST",
    headers: {
      accept: "application/json",
      authorization: `Basic ${Buffer.from(`${cfg.clientID}:${cfg.clientSecret}`).toString("base64")}`,
      "content-type": "application/x-www-form-urlencoded",
    },
    body: new URLSearchParams({
      grant_type: "authorization_code",
      code,
      redirect_uri: cfg.callbackURL,
      code_verifier: transaction.verifier,
    }),
    signal: AbortSignal.timeout(10_000),
  });
  if (!tokenResponse.ok) return errorRedirect(cfg.publicURL, "token_exchange_failed");
  const tokens = (await tokenResponse.json()) as TokenResponse;
  if (!tokens.id_token) return errorRedirect(cfg.publicURL, "id_token_missing");

  let claims: IDTokenClaims;
  try {
    claims = await validateIDToken(tokens.id_token, transaction.nonce, cfg);
  } catch {
    return errorRedirect(cfg.publicURL, "id_token_invalid");
  }
  const session: SealedPostflightSession = {
    subject: claims.sub,
    username: claims.preferred_username || claims.sub,
    ...(claims.email && claims.email_verified === true ? { email: claims.email } : {}),
    ...(claims.name ? { name: claims.name } : {}),
    expiresAt: Date.now() + 30 * 60 * 1000,
    idToken: tokens.id_token,
  };
  const sealedSession = await seal(session, sessionPurpose, cfg.sessionSecret);
  const headers = new Headers({
    ...securityHeaders(),
    location: transaction.returnTo,
  });
  headers.append("set-cookie", clearCookie(AUTH_TRANSACTION_COOKIE));
  headers.append("set-cookie", cookie(SESSION_COOKIE, sealedSession, 30 * 60));
  return new Response(null, { status: 303, headers });
}

export async function readPostflightSession(request: Request): Promise<PostflightSession | null> {
  const value = cookieValue(request, SESSION_COOKIE);
  if (!value) return null;
  try {
    const session = await unseal<SealedPostflightSession>(
      value,
      sessionPurpose,
      configuration().sessionSecret,
    );
    if (session.expiresAt <= Date.now() || !session.subject) return null;
    return {
      subject: session.subject,
      username: session.username,
      ...(session.email ? { email: session.email } : {}),
      ...(session.name ? { name: session.name } : {}),
      expiresAt: session.expiresAt,
    };
  } catch {
    return null;
  }
}

export async function postflightSessionResponse(request: Request): Promise<Response> {
  const session = await readPostflightSession(request);
  return Response.json(
    session ? { authenticated: true, user: session } : { authenticated: false },
    {
      status: session ? 200 : 401,
      headers: {
        ...securityHeaders(),
        "content-type": "application/json; charset=utf-8",
      },
    },
  );
}

export async function endPostflightSession(request: Request): Promise<Response> {
  // Fetch-metadata CSRF guard: logout is reachable by top-level navigation,
  // so a cross-site trigger must not end the session.
  if (request.headers.get("sec-fetch-site") === "cross-site") {
    return new Response(null, { status: 403, headers: securityHeaders() });
  }
  const cfg = configuration();
  const logoutURL = new URL(`${cfg.issuer}/protocol/openid-connect/logout`);
  const params = new URLSearchParams({
    client_id: cfg.clientID,
    post_logout_redirect_uri: `${cfg.publicURL}/postflight`,
  });
  const sealedSession = cookieValue(request, SESSION_COOKIE);
  if (sealedSession) {
    try {
      const session = await unseal<SealedPostflightSession>(
        sealedSession,
        sessionPurpose,
        cfg.sessionSecret,
      );
      if (session.idToken) params.set("id_token_hint", session.idToken);
    } catch {}
  }
  logoutURL.search = params.toString();
  return new Response(null, {
    status: 303,
    headers: {
      ...securityHeaders(),
      location: logoutURL.toString(),
      "set-cookie": clearCookie(SESSION_COOKIE),
    },
  });
}

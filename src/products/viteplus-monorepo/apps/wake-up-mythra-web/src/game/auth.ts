// Sign-in for the dog park: authorization code + PKCE against the
// customer realm's public wake-up-mythra client. Tokens live in
// sessionStorage; the access token (300s life) is refreshed on demand
// before each POST /session mint.

const ISSUER = "https://guardianintelligence.org/realms/guardianintelligence.org";
const CLIENT_ID = "wake-up-mythra";
const AUTH_URL = `${ISSUER}/protocol/openid-connect/auth`;
const TOKEN_URL = `${ISSUER}/protocol/openid-connect/token`;

const store = sessionStorage;

function b64url(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

export async function beginSignIn(): Promise<void> {
  const verifier = b64url(crypto.getRandomValues(new Uint8Array(32)));
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier));
  const state = b64url(crypto.getRandomValues(new Uint8Array(16)));
  store.setItem("pkce_verifier", verifier);
  store.setItem("oauth_state", state);
  const q = new URLSearchParams({
    client_id: CLIENT_ID,
    response_type: "code",
    scope: "openid",
    redirect_uri: location.origin + "/",
    state,
    code_challenge: b64url(new Uint8Array(digest)),
    code_challenge_method: "S256",
  });
  location.assign(`${AUTH_URL}?${q}`);
}

async function tokenGrant(body: URLSearchParams): Promise<boolean> {
  const r = await fetch(TOKEN_URL, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body,
  });
  if (!r.ok) return false;
  const tok = await r.json();
  store.setItem("access_token", tok.access_token);
  store.setItem("access_exp", String(Date.now() + (tok.expires_in - 15) * 1000));
  if (tok.refresh_token) store.setItem("refresh_token", tok.refresh_token);
  return true;
}

// completeSignIn consumes a ?code= callback if one is present.
export async function completeSignIn(): Promise<void> {
  const q = new URLSearchParams(location.search);
  const code = q.get("code");
  if (!code) return;
  const ok =
    q.get("state") === store.getItem("oauth_state") &&
    (await tokenGrant(
      new URLSearchParams({
        grant_type: "authorization_code",
        client_id: CLIENT_ID,
        code,
        redirect_uri: location.origin + "/",
        code_verifier: store.getItem("pkce_verifier") ?? "",
      }),
    ));
  store.removeItem("pkce_verifier");
  store.removeItem("oauth_state");
  if (!ok) {
    store.removeItem("access_token");
    store.removeItem("refresh_token");
  }
  // Drop the code from the URL either way; a stale code is not retryable.
  q.delete("code");
  q.delete("state");
  q.delete("session_state");
  q.delete("iss");
  history.replaceState(null, "", location.pathname + (q.size ? `?${q}` : ""));
}

// accessToken returns a live access token, refreshing when expired.
// null means the user must sign in again.
export async function accessToken(): Promise<string | null> {
  const tok = store.getItem("access_token");
  if (tok && Date.now() < Number(store.getItem("access_exp"))) return tok;
  const refresh = store.getItem("refresh_token");
  if (
    refresh &&
    (await tokenGrant(
      new URLSearchParams({ grant_type: "refresh_token", client_id: CLIENT_ID, refresh_token: refresh }),
    ))
  ) {
    return store.getItem("access_token");
  }
  return null;
}

export function subjectOf(token: string): string {
  try {
    const claims = JSON.parse(atob(token.split(".")[1]!.replace(/-/g, "+").replace(/_/g, "/")));
    return String(claims.sub ?? "");
  } catch {
    return "";
  }
}

// Sign-in for the dog park: authorization code + PKCE against the WUM
// realm's public wake-up-mythra client, always pre-hinted to a broker so
// Keycloak never renders a page — the game page is the provider chooser.
// Desktop signs in through a popup so the live park (and its WebTransport
// session) stays on screen; blocked popups and mobile browsers fall back
// to a full redirect. Tokens live in localStorage so a returning player
// resumes silently within the realm's session window. The client layer is
// UX only: enforcement is the /session mint on the server.

// The dev runner points this at the local issuer (scripts/wum-dev.sh);
// production builds carry the realm.
const ISSUER =
  (import.meta.env?.VITE_OIDC_ISSUER as string | undefined) ??
  "https://auth.wakeupmythra.com/realms/wakeupmythra.com";
const CLIENT_ID = "wake-up-mythra";
const AUTH_URL = `${ISSUER}/protocol/openid-connect/auth`;
const TOKEN_URL = `${ISSUER}/protocol/openid-connect/token`;

export type Provider = "google";

// Storage is reached only inside functions: this module is imported by the
// SSR bundle, where no Web Storage global exists.
function store(): Storage {
  return localStorage;
}

// deviceId is the anonymous spectator's stable pseudonym — the targeting
// key for feature flags, the trace correlate, and the join key that lets
// analytics connect "spectated" to "signed in and checked in". Minted once
// per browser; carries no authority.
export function deviceId(): string {
  let id = store().getItem("wum_device");
  if (!id) {
    id = crypto.randomUUID();
    store().setItem("wum_device", id);
  }
  return id;
}

function b64url(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

async function authRequestURL(provider: Provider): Promise<string> {
  const verifier = b64url(crypto.getRandomValues(new Uint8Array(32)));
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier));
  const state = b64url(crypto.getRandomValues(new Uint8Array(16)));
  store().setItem("pkce_verifier", verifier);
  store().setItem("oauth_state", state);
  const q = new URLSearchParams({
    client_id: CLIENT_ID,
    response_type: "code",
    scope: "openid",
    redirect_uri: location.origin + "/",
    state,
    code_challenge: b64url(new Uint8Array(digest)),
    code_challenge_method: "S256",
    kc_idp_hint: provider,
  });
  return `${AUTH_URL}?${q}`;
}

export type SignInOutcome =
  | { status: "signed-in" }
  | { status: "error"; reason: string }
  | { status: "none" };

// beginSignIn starts a hinted flow. The callback fires only on the popup
// path; the redirect path unloads the page and finishes in the next load's
// completeSignIn.
export function beginSignIn(provider: Provider, onDone: (outcome: SignInOutcome) => void): void {
  void authRequestURL(provider).then((url) => {
    const popup = window.open(url, "wum-signin", "popup,width=480,height=720");
    if (!popup) {
      // Popup blocked (or a mobile browser that treats it as one): carry
      // the current query through the redirect so ?park= and ?spectate
      // survive the round trip.
      store().setItem("signin_return", location.search);
      location.assign(url);
      return;
    }
    let settled = false;
    const settle = (outcome: SignInOutcome) => {
      if (settled) return;
      settled = true;
      window.removeEventListener("message", onMessage);
      clearInterval(watchdog);
      onDone(outcome);
    };
    const onMessage = (e: MessageEvent) => {
      if (e.origin !== location.origin || e.data?.type !== "wum-oauth") return;
      if (e.data.error) {
        settle({ status: "error", reason: String(e.data.error) });
        return;
      }
      if (e.data.state !== store().getItem("oauth_state")) {
        settle({ status: "error", reason: "state mismatch" });
        return;
      }
      void exchangeCode(String(e.data.code)).then(settle);
    };
    window.addEventListener("message", onMessage);
    const watchdog = setInterval(() => {
      if (popup.closed) settle({ status: "error", reason: "sign-in window closed" });
    }, 500);
  });
}

// popupRelay hands an OAuth callback that landed inside the sign-in popup
// back to the opener and closes; true means this page load was the popup
// and the caller should stop booting.
export function popupRelay(): boolean {
  const q = new URLSearchParams(location.search);
  if (!window.opener || (!q.get("code") && !q.get("error"))) return false;
  (window.opener as Window).postMessage(
    { type: "wum-oauth", code: q.get("code"), state: q.get("state"), error: q.get("error") },
    location.origin,
  );
  window.close();
  return true;
}

async function tokenGrant(body: URLSearchParams): Promise<SignInOutcome> {
  let r: Response;
  try {
    r = await fetch(TOKEN_URL, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body,
    });
  } catch {
    return { status: "error", reason: "the sign-in service is unreachable" };
  }
  if (!r.ok) {
    const detail = await r.json().catch(() => ({}));
    return { status: "error", reason: String(detail.error ?? `token endpoint ${r.status}`) };
  }
  const tok = await r.json();
  store().setItem("access_token", tok.access_token);
  store().setItem("access_exp", String(Date.now() + (tok.expires_in - 15) * 1000));
  if (tok.refresh_token) store().setItem("refresh_token", tok.refresh_token);
  return { status: "signed-in" };
}

async function exchangeCode(code: string): Promise<SignInOutcome> {
  const outcome = await tokenGrant(
    new URLSearchParams({
      grant_type: "authorization_code",
      client_id: CLIENT_ID,
      code,
      redirect_uri: location.origin + "/",
      code_verifier: store().getItem("pkce_verifier") ?? "",
    }),
  );
  store().removeItem("pkce_verifier");
  store().removeItem("oauth_state");
  return outcome;
}

// completeSignIn consumes a redirect-flow callback: a ?code=, an OAuth
// ?error=, or an ?auth_error= from the wum-bounce theme. Either way the
// params are dropped from the URL (a stale code is not retryable) and any
// stored pre-sign-in query is restored so ?park= and ?spectate survive.
export async function completeSignIn(): Promise<SignInOutcome> {
  const q = new URLSearchParams(location.search);
  const code = q.get("code");
  const state = q.get("state");
  const authError = q.get("error") ?? q.get("auth_error");
  if (!code && !authError) return { status: "none" };

  for (const p of [
    "code",
    "state",
    "session_state",
    "iss",
    "error",
    "error_description",
    "auth_error",
  ]) {
    q.delete(p);
  }
  const stored = store().getItem("signin_return");
  store().removeItem("signin_return");
  const restored = new URLSearchParams(stored ?? "");
  for (const [k, v] of q) restored.set(k, v);
  const search = restored.toString();
  history.replaceState(null, "", location.pathname + (search ? `?${search}` : ""));

  if (authError) {
    store().removeItem("pkce_verifier");
    store().removeItem("oauth_state");
    return {
      status: "error",
      reason:
        authError === "interrupted"
          ? "sign-in was interrupted — try again"
          : "sign-in was cancelled",
    };
  }
  if (!state || state !== store().getItem("oauth_state")) {
    store().removeItem("pkce_verifier");
    store().removeItem("oauth_state");
    return { status: "error", reason: "sign-in session expired — try again" };
  }
  const outcome = await exchangeCode(code!);
  if (outcome.status === "error") {
    store().removeItem("access_token");
    store().removeItem("refresh_token");
  }
  return outcome;
}

// accessToken returns a live access token, refreshing when expired.
// null means anonymous: the caller falls back to spectating.
export async function accessToken(): Promise<string | null> {
  const tok = store().getItem("access_token");
  if (tok && Date.now() < Number(store().getItem("access_exp"))) return tok;
  const refresh = store().getItem("refresh_token");
  if (
    refresh &&
    (await tokenGrant(
      new URLSearchParams({
        grant_type: "refresh_token",
        client_id: CLIENT_ID,
        refresh_token: refresh,
      }),
    ).then((o) => o.status === "signed-in"))
  ) {
    return store().getItem("access_token");
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

import { useCallback, useEffect, useReducer, useRef } from "react";
import * as v from "valibot";

// Sign in with Google, run entirely in the browser: OpenID Connect's implicit
// flow (response_type=id_token) in a popup. The popup lands on
// /oauth/google.html, which hands the URL fragment back over a
// BroadcastChannel (so it still arrives if Google's pages severed
// window.opener). The page checks state, then verifies the ID token itself:
// RS256 signature against Google's published keys, issuer, audience, expiry
// and nonce. A backend that later acts on the guest's identity must verify
// the token again; this check only decides what the page shows.

// The anveio-cal project's web client (Google Auth Platform). Client IDs are
// public; VITE_GOOGLE_CLIENT_ID overrides it.
const CLIENT_ID: string =
  import.meta.env.VITE_GOOGLE_CLIENT_ID ??
  "494052463155-g1fnlk7ep4pipg66fnn7i3pgudp2rec4.apps.googleusercontent.com";
const REDIRECT = "/oauth/google.html";
export const CHANNEL = "rumi-google-oauth";
const ISSUERS = ["https://accounts.google.com", "accounts.google.com"];
// How long after the popup closes a late result may still arrive.
const CLOSE_GRACE_MS = 600;

export type GoogleUser = {
  readonly sub: string;
  readonly email: string;
  readonly name: string;
  readonly picture: string | undefined;
};

export type Failure =
  | "cancelled" // the guest closed the window or declined
  | "blocked" // the browser blocked the popup
  | "invalid"; // Google answered, but the answer didn't check out

export type AuthState =
  | { readonly status: "signedOut" }
  | { readonly status: "waiting" } // the Google window is open
  | { readonly status: "verifying" } // Google answered; checking the token
  | { readonly status: "signedIn"; readonly user: GoogleUser }
  | { readonly status: "failed"; readonly failure: Failure };

export type AuthEvent =
  | { readonly type: "open" }
  | { readonly type: "answered" }
  | { readonly type: "verified"; readonly user: GoogleUser }
  | { readonly type: "fail"; readonly failure: Failure }
  | { readonly type: "reset" };

export function authReducer(state: AuthState, e: AuthEvent): AuthState {
  switch (e.type) {
    case "open":
      return state.status === "signedIn" ? state : { status: "waiting" };
    case "answered":
      return state.status === "waiting" ? { status: "verifying" } : state;
    case "verified":
      return state.status === "verifying" ? { status: "signedIn", user: e.user } : state;
    case "fail":
      return state.status === "signedIn" ? state : { status: "failed", failure: e.failure };
    case "reset":
      return state.status === "signedIn" ? state : { status: "signedOut" };
  }
}

// ---------- the ID token ----------

const Claims = v.object({
  iss: v.picklist(ISSUERS),
  aud: v.string(),
  sub: v.string(),
  exp: v.number(),
  nonce: v.string(),
  email: v.string(),
  name: v.optional(v.string()),
  picture: v.optional(v.string()),
});
const Header = v.object({ alg: v.literal("RS256"), kid: v.string() });
const Jwks = v.object({
  keys: v.array(
    v.looseObject({ kid: v.string(), kty: v.literal("RSA"), n: v.string(), e: v.string() }),
  ),
});

const b64url = (s: string) => {
  const b = atob(
    s
      .replace(/-/g, "+")
      .replace(/_/g, "/")
      .padEnd(Math.ceil(s.length / 4) * 4, "="),
  );
  return Uint8Array.from(b, (c) => c.charCodeAt(0));
};
const json = (s: string): unknown => JSON.parse(new TextDecoder().decode(b64url(s)));

async function verify(token: string, nonce: string, clientId: string): Promise<GoogleUser | null> {
  const [h, p, sig] = token.split(".");
  if (!h || !p || !sig) return null;
  const header = v.safeParse(Header, json(h));
  const claims = v.safeParse(Claims, json(p));
  if (!header.success || !claims.success) return null;
  const c = claims.output;
  if (c.aud !== clientId || c.nonce !== nonce || c.exp * 1000 < Date.now() - 60_000) return null;

  const res = await fetch("https://www.googleapis.com/oauth2/v3/certs");
  const jwks = v.safeParse(Jwks, await res.json());
  const jwk = jwks.success && jwks.output.keys.find((k) => k.kid === header.output.kid);
  if (!jwk) return null;
  const key = await crypto.subtle.importKey(
    "jwk",
    { kty: "RSA", n: jwk.n, e: jwk.e, alg: "RS256", ext: true },
    { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" },
    false,
    ["verify"],
  );
  const ok = await crypto.subtle.verify(
    "RSASSA-PKCS1-v1_5",
    key,
    b64url(sig),
    new TextEncoder().encode(`${h}.${p}`),
  );
  if (!ok) return null;
  return { sub: c.sub, email: c.email, name: c.name ?? c.email, picture: c.picture };
}

// ---------- the popup ----------

const random = () => {
  const b = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
};

const Answer = v.object({ state: v.string(), fragment: v.string() });

type Attempt = { state: string; nonce: string; popup: Window; poll: number; closedAt?: number };

/**
 * The sign-in state machine. `open` must run inside the tap's handler, or the
 * browser blocks the popup. `cancel` closes the window and returns to
 * signedOut; `reset` clears a failure.
 */
export function useGoogleSignIn() {
  const [state, dispatch] = useReducer(authReducer, { status: "signedOut" });
  const attempt = useRef<Attempt | null>(null);

  const end = useCallback(() => {
    const a = attempt.current;
    if (!a) return;
    window.clearInterval(a.poll);
    attempt.current = null;
  }, []);

  const finish = useCallback(
    async (a: Attempt, fragment: string) => {
      end();
      if (!a.popup.closed) a.popup.close();
      const q = new URLSearchParams(fragment.replace(/^#/, ""));
      const err = q.get("error");
      const token = q.get("id_token");
      if (err || !token) {
        dispatch({ type: "fail", failure: err === "access_denied" ? "cancelled" : "invalid" });
        return;
      }
      dispatch({ type: "answered" });
      const user = await verify(token, a.nonce, CLIENT_ID).catch(() => null);
      dispatch(user ? { type: "verified", user } : { type: "fail", failure: "invalid" });
    },
    [end],
  );

  useEffect(() => {
    const ch = new BroadcastChannel(CHANNEL);
    ch.onmessage = (e: MessageEvent) => {
      const a = attempt.current;
      const msg = v.safeParse(Answer, e.data);
      if (a && msg.success && msg.output.state === a.state) void finish(a, msg.output.fragment);
    };
    return () => {
      ch.close();
      end();
    };
  }, [finish, end]);

  const open = useCallback(() => {
    if (attempt.current && !attempt.current.popup.closed) {
      attempt.current.popup.focus();
      return;
    }
    end();
    const state = random();
    const nonce = random();
    const url = new URL("https://accounts.google.com/o/oauth2/v2/auth");
    url.search = new URLSearchParams({
      client_id: CLIENT_ID,
      redirect_uri: new URL(REDIRECT, location.origin).href,
      response_type: "id_token",
      scope: "openid email profile",
      prompt: "select_account",
      state,
      nonce,
    }).toString();
    const w = 480;
    const h = 640;
    const left = Math.round(window.screenX + (window.outerWidth - w) / 2);
    const top = Math.round(window.screenY + (window.outerHeight - h) / 2);
    const popup = window.open(
      url,
      "rumi-google",
      `popup,width=${w},height=${h},left=${left},top=${top}`,
    );
    if (!popup) {
      dispatch({ type: "fail", failure: "blocked" });
      return;
    }
    dispatch({ type: "open" });
    // A window the guest closed without answering is a cancel, once a late
    // answer has had a moment to arrive.
    const a: Attempt = { state, nonce, popup, poll: 0 };
    a.poll = window.setInterval(() => {
      if (!popup.closed) return;
      a.closedAt ??= Date.now();
      if (Date.now() - a.closedAt < CLOSE_GRACE_MS) return;
      end();
      dispatch({ type: "fail", failure: "cancelled" });
    }, 250);
    attempt.current = a;
  }, [end]);

  const cancel = useCallback(() => {
    const a = attempt.current;
    end();
    if (a && !a.popup.closed) a.popup.close();
    dispatch({ type: "reset" });
  }, [end]);

  const reset = useCallback(() => dispatch({ type: "reset" }), []);

  return { state, open, cancel, reset } as const;
}

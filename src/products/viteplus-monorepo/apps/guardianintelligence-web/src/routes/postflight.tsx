import { createFileRoute } from "@tanstack/react-router";
import { createServerFn } from "@tanstack/react-start";
import { useEffect, useState } from "react";
import "~/styles/postflight.css";

type Session = {
  readonly authenticated: boolean;
  readonly user?: {
    readonly username: string;
    readonly name?: string;
  };
};

function PostflightMark({ small = false }: { readonly small?: boolean }) {
  return (
    <svg
      aria-hidden="true"
      className={small ? "postflight-mark postflight-mark--small" : "postflight-mark"}
      viewBox="0 0 44 44"
    >
      <path d="M22 4.5a17.5 17.5 0 1 0 17.5 17.5" />
      <path d="M14 24.5 21 31l15-19" />
      <circle cx="36" cy="12" r="4" />
    </svg>
  );
}

const postflightAvailability = createServerFn({ method: "GET" }).handler(() => ({
  enabled: process.env.POSTFLIGHT_AUTH_ENABLED === "true",
}));

function LoginCard({
  session,
  authEnabled,
}: {
  readonly session: Session | null;
  readonly authEnabled: boolean;
}) {
  const signedIn = session?.authenticated;
  const label = session?.user?.name || session?.user?.username;
  return (
    <div className="postflight-login-card" data-postflight-login-card>
      <div className="postflight-card-logo">
        <PostflightMark small />
      </div>
      <h2>{signedIn ? `Welcome, ${label}` : "Sign in to Postflight"}</h2>
      <p>
        {signedIn
          ? "Your Guardian account is connected."
          : "Run GitHub CI on warm, isolated infrastructure."}
      </p>
      <a
        id={authEnabled ? (signedIn ? "postflight-logout" : "postflight-sign-in") : undefined}
        aria-disabled={!authEnabled}
        className={`postflight-guardian-button${authEnabled ? "" : " postflight-guardian-button--disabled"}`}
        href={
          authEnabled
            ? signedIn
              ? "/postflight/auth/logout"
              : "/postflight/auth/login"
            : undefined
        }
      >
        {!signedIn && <PostflightMark small />}
        {signedIn ? "Sign out" : authEnabled ? "Sign in with Guardian" : "Coming soon"}
      </a>
      {signedIn && authEnabled ? (
        <a
          className="postflight-account-link"
          href="/realms/guardianintelligence.org/account/#/linked-accounts"
        >
          Manage connected accounts
        </a>
      ) : (
        <small>Use a social account connected to Guardian.</small>
      )}
    </div>
  );
}

function PostflightPage() {
  const { authEnabled } = Route.useLoaderData();
  const [session, setSession] = useState<Session | null>(null);
  useEffect(() => {
    if (!authEnabled) return;
    let active = true;
    fetch("/postflight/auth/session", { credentials: "same-origin" })
      .then(async (response) => (await response.json()) as Session)
      .then((value) => {
        if (active) setSession(value);
      })
      .catch(() => undefined);
    return () => {
      active = false;
    };
  }, [authEnabled]);

  return (
    <div className="postflight-page" data-postflight-oobe="ready">
      <header className="postflight-nav">
        <a className="postflight-wordmark" href="/postflight" aria-label="Postflight home">
          <PostflightMark small />
          <span>Postflight</span>
        </a>
        <a
          aria-disabled={!authEnabled}
          className="postflight-nav-cta"
          href={authEnabled ? "/postflight/auth/login" : undefined}
        >
          {authEnabled ? "Get started" : "Coming soon"}
        </a>
      </header>

      <main id="main">
        <section className="postflight-hero">
          <div className="postflight-light postflight-light--one" />
          <div className="postflight-light postflight-light--two" />
          <p className="postflight-intro">
            Introducing <PostflightMark small /> <strong>Postflight</strong>
          </p>
          <h1>
            The fastest route
            <br />
            from push to green.
          </h1>
          <p className="postflight-deck">
            Warm, isolated GitHub runners that turn CI from a queue into feedback.
          </p>
          <LoginCard session={session} authEnabled={authEnabled} />
          <div className="postflight-orbit" aria-hidden="true">
            <span />
            <span />
            <span />
          </div>
        </section>

        <section className="postflight-feature-strip" aria-label="Postflight capabilities">
          {[
            ["01", "Warm runners"],
            ["02", "GitHub-native"],
            ["03", "Isolated jobs"],
            ["04", "Fast feedback"],
            ["05", "Guardian identity"],
          ].map(([number, label]) => (
            <article key={number}>
              <span>{number}</span>
              <div className="postflight-feature-glyph" aria-hidden="true" />
              <h2>{label}</h2>
            </article>
          ))}
        </section>

        <section className="postflight-dark-section">
          <p className="postflight-kicker">Built for the critical path</p>
          <h2>
            Your code. Your runners.
            <br />
            Minutes returned every day.
          </h2>
          <div className="postflight-terminal">
            <div>
              <i />
              <i />
              <i />
              <span>guardian/postflight</span>
            </div>
            <code>
              <em>$</em> git push
              <br />
              <b>✓</b> runner ready <span>0.8s</span>
              <br />
              <b>✓</b> checkout restored <span>1.4s</span>
              <br />
              <b>✓</b> checks complete <span>18.7s</span>
            </code>
          </div>
        </section>

        <section className="postflight-final">
          <PostflightMark />
          <h2>Ship while the context is still warm.</h2>
          <a aria-disabled={!authEnabled} href={authEnabled ? "/postflight/auth/login" : undefined}>
            {authEnabled ? "Sign in with Guardian" : "Coming soon"}
          </a>
        </section>
      </main>

      <footer className="postflight-footer">
        <span>© 2026 Guardian Intelligence LLC</span>
        <a href="https://x.com/guardians_llc" rel="noreferrer">
          Follow @guardians_llc on X
        </a>
      </footer>
    </div>
  );
}

export const Route = createFileRoute("/postflight")({
  component: PostflightPage,
  loader: async () => {
    const availability = await postflightAvailability();
    return { authEnabled: availability.enabled };
  },
  head: () => ({
    meta: [
      { title: "Postflight — faster GitHub CI" },
      {
        name: "description",
        content: "Warm, isolated GitHub runners that turn CI from a queue into feedback.",
      },
      { name: "theme-color", content: "#5d2df5" },
    ],
  }),
});

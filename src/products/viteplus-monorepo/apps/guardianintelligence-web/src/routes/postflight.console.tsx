import { createFileRoute, redirect } from "@tanstack/react-router";
import { createServerFn } from "@tanstack/react-start";
import { getRequest } from "@tanstack/react-start/server";
import { useState } from "react";
import { readPostflightSession } from "~/lib/postflight-auth";
import "~/styles/postflight.css";

const consoleSession = createServerFn({ method: "GET" }).handler(async () => {
  if (process.env.POSTFLIGHT_AUTH_ENABLED !== "true") return null;
  const session = await readPostflightSession(getRequest());
  if (!session) return null;
  return { username: session.username, name: session.name ?? null };
});

const agentTools = ["Claude Code", "Codex", "Gemini CLI"] as const;

function setupSnippet(tool: string): string {
  return `# Postflight setup (${tool})\nThe Postflight CLI is coming soon. Instructions will appear at https://guardianintelligence.org/postflight/console.`;
}

function CopyButton({ tool }: { readonly tool: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      className="postflight-copy-button"
      type="button"
      onClick={() => {
        navigator.clipboard
          .writeText(setupSnippet(tool))
          .then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 2000);
          })
          .catch(() => undefined);
      }}
    >
      {copied ? "Copied" : `Copy for ${tool}`}
    </button>
  );
}

function PostflightConsolePage() {
  const session = Route.useLoaderData();
  return (
    <div className="postflight-page" data-postflight-console="ready">
      <header className="postflight-nav">
        <a className="postflight-wordmark" href="/postflight" aria-label="Postflight home">
          <span>Postflight</span>
        </a>
        <a id="postflight-logout" className="postflight-nav-cta" href="/postflight/auth/logout">
          Sign out
        </a>
      </header>
      <main id="main" className="postflight-console">
        <h1>Console</h1>
        <p className="postflight-console-user">Signed in as {session.name || session.username}</p>
        <p>To get started with Postflight, install the CLI:</p>
        <code className="postflight-console-code">coming soon</code>
        <div className="postflight-console-copies">
          {agentTools.map((tool) => (
            <CopyButton key={tool} tool={tool} />
          ))}
        </div>
      </main>
    </div>
  );
}

export const Route = createFileRoute("/postflight/console")({
  component: PostflightConsolePage,
  loader: async () => {
    const session = await consoleSession();
    if (!session) throw redirect({ to: "/postflight", replace: true });
    return session;
  },
  head: () => ({
    meta: [{ title: "Postflight console" }, { name: "robots", content: "noindex" }],
  }),
});

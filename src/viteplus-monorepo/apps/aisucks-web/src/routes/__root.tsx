import { createRootRoute, HeadContent, Outlet } from "@tanstack/react-router";
import type { ReactNode } from "react";
import appCss from "~/styles/app.css?url";

export const Route = createRootRoute({
  head: () => ({
    meta: [
      { charSet: "utf-8" },
      { name: "viewport", content: "width=device-width, initial-scale=1" },
      { title: "aisucks.app" },
      // The charter's epigraph (docs/aisucks/charter.md) — approved copy.
      {
        name: "description",
        content: "If AI is gonna say sh*t, it should at least be right about it.",
      },
    ],
    links: [{ rel: "stylesheet", href: appCss }],
  }),
  component: RootComponent,
});

function RootComponent() {
  return (
    <RootDocument>
      <Outlet />
    </RootDocument>
  );
}

// No <Scripts /> on purpose: the served page carries zero JavaScript (charter
// value 5). emit-static.mjs strips anything the framework still injects, and
// aisucks' Go tests fail if a <script> tag ever reaches the page.
function RootDocument({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <head>
        <HeadContent />
      </head>
      <body className="bg-white text-neutral-900 antialiased">{children}</body>
    </html>
  );
}

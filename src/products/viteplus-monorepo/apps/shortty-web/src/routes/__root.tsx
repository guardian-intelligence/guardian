import { createRootRoute, HeadContent, Outlet, Scripts } from "@tanstack/react-router";
import { TelemetryProbe } from "~/lib/telemetry/page-view";
import { deployMetaTags } from "~/lib/telemetry/server-deploy-meta";
import "~/styles/app.css";

export const Route = createRootRoute({
  component: RootComponent,
  head: () => ({
    meta: [
      { charSet: "utf-8" },
      { name: "viewport", content: "width=device-width, initial-scale=1" },
      { name: "theme-color", content: "#0a0a0e" },
      { title: "Shortty — any clip, under 4 MB" },
      {
        name: "description",
        content:
          "Pick up to a minute of any video and get the best possible quality under 4 MB. In your browser — your video never leaves your device.",
      },
      { property: "og:site_name", content: "Shortty" },
      ...deployMetaTags(),
    ],
    links: [
      { rel: "icon", type: "image/svg+xml", href: "/favicon.svg" },
      {
        rel: "preload",
        href: "/fonts/Geist-Variable.woff2",
        as: "font",
        type: "font/woff2",
        crossOrigin: "anonymous",
      },
    ],
  }),
});

function RootComponent() {
  return (
    <html lang="en">
      <head>
        <HeadContent />
      </head>
      <body className="font-sans antialiased text-mist min-h-screen">
        <div className="stage-light" aria-hidden="true">
          <div className="stage-spotlights">
            <span className="stage-spotlight stage-spotlight--left" />
            <span className="stage-spotlight stage-spotlight--center" />
            <span className="stage-spotlight stage-spotlight--right" />
          </div>
        </div>
        <div className="stage-grid" aria-hidden="true" />
        <div className="stage-grain" aria-hidden="true" />
        <Outlet />
        <TelemetryProbe />
        <Scripts />
      </body>
    </html>
  );
}

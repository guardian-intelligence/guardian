import { createRootRoute, HeadContent, Outlet, Scripts } from "@tanstack/react-router";
import { BackgroundParticles } from "~/components/background-particles";
import { TelemetryProbe } from "~/lib/telemetry/page-view";
import { deployMetaTags } from "~/lib/telemetry/server-deploy-meta";
import "~/styles/app.css";

export const Route = createRootRoute({
  component: RootComponent,
  head: () => ({
    meta: [
      { charSet: "utf-8" },
      // Media elements have no referrerpolicy attribute, so the document
      // policy is what keeps the <video> preview's requests Referer-free —
      // video.twimg.com 403s any third-party Referer. (Worker fetches set
      // this per-request in engine/probe.ts; this tag cannot reach them.)
      { name: "referrer", content: "no-referrer" },
      {
        name: "viewport",
        content: "width=device-width, initial-scale=1, viewport-fit=cover",
      },
      { name: "theme-color", content: "#0a0a0e" },
      { name: "apple-mobile-web-app-capable", content: "yes" },
      { name: "apple-mobile-web-app-title", content: "PrivateCut" },
      { name: "apple-mobile-web-app-status-bar-style", content: "black-translucent" },
      { title: "PrivateCut — private video clipping, on your device" },
      {
        name: "description",
        content:
          "Trim and compress any video right in your browser. Nothing is uploaded — your footage never leaves your device. No account, no cloud; export a clip under 4 MB.",
      },
      { property: "og:site_name", content: "PrivateCut" },
      ...deployMetaTags(),
    ],
    links: [
      { rel: "icon", type: "image/svg+xml", href: "/favicon.svg" },
      { rel: "icon", type: "image/png", sizes: "192x192", href: "/icon-192.png" },
      { rel: "apple-touch-icon", sizes: "180x180", href: "/apple-touch-icon.png" },
      { rel: "manifest", href: "/manifest.webmanifest" },
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
        <BackgroundParticles />
        <div className="stage-lines" aria-hidden="true">
          <span className="stage-line stage-line--vertical stage-line--outer-left" />
          <span className="stage-line stage-line--vertical stage-line--inner-left" />
          <span className="stage-line stage-line--vertical stage-line--center" />
          <span className="stage-line stage-line--vertical stage-line--inner-right" />
          <span className="stage-line stage-line--vertical stage-line--outer-right" />
          <span className="stage-line stage-line--horizontal stage-line--header" />
        </div>
        <div className="stage-grain" aria-hidden="true" />
        <Outlet />
        <TelemetryProbe />
        <Scripts />
      </body>
    </html>
  );
}

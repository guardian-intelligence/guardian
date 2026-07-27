import { createRootRoute, HeadContent, Outlet, Scripts } from "@tanstack/react-router";
import { CanvasDocument } from "~/components/canvas-document";
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
          "Trim and compress any video right in your browser. Nothing is uploaded — your footage never leaves your device. No account, no cloud; export a clip under the size cap you pick, from 4 MB to 100 MB.",
      },
      { property: "og:site_name", content: "PrivateCut" },
      ...deployMetaTags(),
    ],
    links: [
      {
        rel: "icon",
        type: "image/svg+xml",
        sizes: "any",
        href: "/favicon.svg?v=5fac7acaba97",
      },
      {
        rel: "alternate icon",
        type: "image/x-icon",
        href: "/favicon.ico?v=eb1b6385402e",
      },
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
        <CanvasDocument>
          <Outlet />
        </CanvasDocument>
        <TelemetryProbe />
        <Scripts />
      </body>
    </html>
  );
}

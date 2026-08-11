import { createRootRoute, HeadContent, Outlet, Scripts } from "@tanstack/react-router";
import { CanvasDocument } from "~/components/canvas-document";
import { Toaster } from "~/components/ui/sonner";
import { SITE_DESCRIPTION, SITE_TITLE, SITE_URL } from "~/lib/site";
import { TelemetryProbe } from "@guardian/telemetry/probe";
import { deployMetaTags } from "@guardian/telemetry/server-deploy-meta";
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
      { title: SITE_TITLE },
      { name: "description", content: SITE_DESCRIPTION },
      { property: "og:site_name", content: "PrivateCut" },
      { property: "og:type", content: "website" },
      { property: "og:url", content: `${SITE_URL}/` },
      { property: "og:title", content: SITE_TITLE },
      { property: "og:description", content: SITE_DESCRIPTION },
      // Absolute URL: Facebook, LinkedIn, and some aggregators silently drop
      // relative og:image values. PNG because X/Twitter and Facebook do not
      // render SVG card images.
      { property: "og:image", content: `${SITE_URL}/og.png` },
      { property: "og:image:type", content: "image/png" },
      { property: "og:image:width", content: "1200" },
      { property: "og:image:height", content: "630" },
      { property: "og:image:alt", content: "PrivateCut — Private Cutting Room Floor" },
      { name: "twitter:card", content: "summary_large_image" },
      { name: "twitter:title", content: SITE_TITLE },
      { name: "twitter:description", content: SITE_DESCRIPTION },
      { name: "twitter:image", content: `${SITE_URL}/og.png` },
      { name: "twitter:image:alt", content: "PrivateCut — Private Cutting Room Floor" },
      ...deployMetaTags(),
    ],
    links: [
      { rel: "canonical", href: `${SITE_URL}/` },
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
        <Toaster />
        <TelemetryProbe app="privatecut" />
        <Scripts />
      </body>
    </html>
  );
}

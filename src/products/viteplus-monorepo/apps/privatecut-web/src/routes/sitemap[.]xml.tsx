import { createFileRoute } from "@tanstack/react-router";
import { SITE_URL } from "~/lib/site";

// One page, one URL. Hand-rolled XML for the same reason as the company
// site's sitemap: the format is a few lines and a sitemap library is more
// surface than a single-URL product should import.

const SITEMAP_HEADERS = {
  "content-type": "application/xml; charset=utf-8",
  "cache-control": "public, max-age=600, s-maxage=600",
} as const;

const SITEMAP = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>${SITE_URL}/</loc></url>
</urlset>
`;

export const Route = createFileRoute("/sitemap.xml")({
  server: {
    handlers: {
      HEAD: () => new Response(null, { status: 200, headers: SITEMAP_HEADERS }),
      GET: () => new Response(SITEMAP, { status: 200, headers: SITEMAP_HEADERS }),
    },
  },
});

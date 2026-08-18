import { createFileRoute } from "@tanstack/react-router";

const body = `# HELP company_site_build_info Company site build information.
# TYPE company_site_build_info gauge
company_site_build_info{app="company-site",runtime="tanstack-start"} 1
`;

const headers = {
  "cache-control": "no-store",
  "content-type": "text/plain; version=0.0.4; charset=utf-8",
} as const;

export const Route = createFileRoute("/metrics")({
  server: {
    handlers: {
      HEAD: () => new Response(null, { status: 200, headers }),
      GET: () => new Response(body, { status: 200, headers }),
    },
  },
});

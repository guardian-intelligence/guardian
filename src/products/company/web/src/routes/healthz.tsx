import { createFileRoute } from "@tanstack/react-router";

const headers = {
  "cache-control": "no-store",
  "content-type": "text/plain; charset=utf-8",
} as const;

export const Route = createFileRoute("/healthz")({
  server: {
    handlers: {
      HEAD: () => new Response(null, { status: 200, headers }),
      GET: () => new Response("ok\n", { status: 200, headers }),
    },
  },
});

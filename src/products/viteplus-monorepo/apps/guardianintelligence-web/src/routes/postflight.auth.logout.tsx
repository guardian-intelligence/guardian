import { createFileRoute } from "@tanstack/react-router";
import { endPostflightSession } from "~/lib/postflight-auth";

export const Route = createFileRoute("/postflight/auth/logout")({
  server: {
    handlers: {
      GET: ({ request }) => endPostflightSession(request),
      POST: ({ request }) => endPostflightSession(request),
    },
  },
});

import { createFileRoute } from "@tanstack/react-router";
import { completePostflightLogin } from "~/lib/postflight-auth";

export const Route = createFileRoute("/postflight/auth/callback")({
  server: {
    handlers: {
      GET: ({ request }) => completePostflightLogin(request),
    },
  },
});

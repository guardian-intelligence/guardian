import { createFileRoute } from "@tanstack/react-router";
import { beginPostflightLogin } from "~/lib/postflight-auth";

export const Route = createFileRoute("/postflight/auth/login")({
  server: {
    handlers: {
      GET: ({ request }) => beginPostflightLogin(request),
    },
  },
});

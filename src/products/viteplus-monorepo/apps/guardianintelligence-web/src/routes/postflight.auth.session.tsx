import { createFileRoute } from "@tanstack/react-router";
import { postflightSessionResponse } from "~/lib/postflight-auth";

export const Route = createFileRoute("/postflight/auth/session")({
  server: {
    handlers: {
      GET: ({ request }) => postflightSessionResponse(request),
    },
  },
});

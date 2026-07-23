import { createFileRoute } from "@tanstack/react-router";
import { deviceContinueRedirect } from "~/lib/postflight-auth";

export const Route = createFileRoute("/postflight/device/continue")({
  server: {
    handlers: {
      GET: ({ request }) => deviceContinueRedirect(request),
    },
  },
});

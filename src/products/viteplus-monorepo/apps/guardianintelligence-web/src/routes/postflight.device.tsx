import { createFileRoute } from "@tanstack/react-router";
import { deviceApprovalRedirect } from "~/lib/postflight-auth";

export const Route = createFileRoute("/postflight/device")({
  server: {
    handlers: {
      GET: ({ request }) => deviceApprovalRedirect(request),
    },
  },
});

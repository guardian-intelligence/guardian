import { createFileRoute } from "@tanstack/react-router";
import { handle } from "~/upload/footage";

export const Route = createFileRoute("/upload/api/$op")({
  server: {
    handlers: {
      POST: ({ request, params }) => handle(params.op, request),
    },
  },
});

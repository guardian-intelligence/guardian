import { createFileRoute } from "@tanstack/react-router";
import { Editor } from "~/design/Editor";

// The home page's type editor. It drives live frames of the page, so it
// renders in the browser only.
export const Route = createFileRoute("/design")({
  ssr: false,
  component: Editor,
  head: () => ({
    meta: [
      { title: "Type — Anveio design" },
      { name: "robots", content: "noindex, nofollow, noarchive" },
      { name: "theme-color", content: "#1e1e1e" },
    ],
  }),
});

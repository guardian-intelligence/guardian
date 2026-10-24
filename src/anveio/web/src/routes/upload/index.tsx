import { createFileRoute } from "@tanstack/react-router";
import { Upload } from "~/upload/Upload";
import "~/styles/upload.css";

export const Route = createFileRoute("/upload/")({
  component: Upload,
  ssr: false,
  head: () => ({
    meta: [
      { title: "Anveio — ভিডিও পাঠান" },
      { name: "robots", content: "noindex, nofollow, noarchive" },
      { name: "referrer", content: "no-referrer" },
      { name: "theme-color", content: "#0b3b2c" },
    ],
  }),
});

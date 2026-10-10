import { createFileRoute } from "@tanstack/react-router";
import { Deck } from "~/deck/Deck";

export const dataHead = {
  meta: [
    { title: "Anveio Data" },
    { name: "robots", content: "noindex, nofollow, noarchive" },
    { name: "description", content: "Anveio Data investor preview." },
  ],
};

export const Route = createFileRoute("/data")({
  component: Deck,
  head: () => dataHead,
});

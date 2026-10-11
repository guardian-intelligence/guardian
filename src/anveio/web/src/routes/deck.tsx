import { createFileRoute } from "@tanstack/react-router";
import { Deck } from "~/deck/Deck";

export const deckHead = {
  meta: [
    { title: "Anveio" },
    { name: "robots", content: "noindex, nofollow, noarchive" },
    { name: "description", content: "Anveio investor deck." },
  ],
};

export const Route = createFileRoute("/deck")({
  component: Deck,
  head: () => deckHead,
});

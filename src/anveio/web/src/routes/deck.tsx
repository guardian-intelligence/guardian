import { createFileRoute } from "@tanstack/react-router";
import preview from "~/deck/social-preview.json";
import { Deck } from "~/deck/Deck";

const title = "Anveio — The Data Company of Bangladesh";
const description = "Anveio investor deck.";
const url = "https://anveio.com/deck";
const image = `https://anveio.com${preview.image}`;

export const deckHead = {
  meta: [
    { title },
    { name: "robots", content: "noindex, nofollow, noarchive" },
    { name: "description", content: description },
    { property: "og:type", content: "website" },
    { property: "og:url", content: url },
    { property: "og:title", content: title },
    { property: "og:description", content: description },
    { property: "og:image", content: image },
    { property: "og:image:type", content: "image/png" },
    { property: "og:image:width", content: String(preview.width) },
    { property: "og:image:height", content: String(preview.height) },
    { property: "og:image:alt", content: title },
    { name: "twitter:card", content: "summary_large_image" },
    { name: "twitter:title", content: title },
    { name: "twitter:description", content: description },
    { name: "twitter:image", content: image },
    { name: "twitter:image:alt", content: title },
  ],
  links: [{ rel: "canonical", href: url }],
};

export const Route = createFileRoute("/deck")({
  component: Deck,
  head: () => deckHead,
});

// Centralized head helpers. Every route runs its title/description/og/twitter
// meta through ogMeta() so (a) no route ships without a social card and (b)
// the og:image URL is absolute — Facebook, LinkedIn, and some aggregators
// silently drop relative og:image values. The slug must exist in og/catalog.ts;
// the dynamic /og/$slug endpoint serves the generated SVG card.
//
// X/Twitter and Facebook do not render SVG card images at all. Routes whose
// links get shared set `imageFormat: "png"`, which points the card at a
// pre-rendered artifact under public/og-png/<slug>.png (rasterized from the
// live SVG endpoint — 1200×630, fonts embedded) rather than putting a native
// rasterizer in the request path. Adding a shareable route = render its card
// to public/og-png/ and flip the flag.

const SITE_URL = "https://guardianintelligence.org";

export interface OGMetaInput {
  readonly slug: string;
  readonly title: string;
  readonly description: string;
  // "article" for long-form (letters, news posts); defaults to "website".
  readonly type?: "website" | "article";
  // Route path (e.g. "/letters/dear-shovon") — emits og:url. Pair with a
  // canonical <link> via canonicalLink() in the route's head links.
  readonly path?: string;
  // "png" serves the pre-rendered public/og-png artifact (required for
  // X/Twitter and Facebook); "svg" (default) serves the live endpoint.
  readonly imageFormat?: "svg" | "png";
}

export interface MetaTag {
  readonly title?: string;
  readonly name?: string;
  readonly property?: string;
  readonly content?: string;
}

export function ogMeta(input: OGMetaInput): MetaTag[] {
  const png = input.imageFormat === "png";
  const imageURL = png ? `${SITE_URL}/og-png/${input.slug}.png` : `${SITE_URL}/og/${input.slug}`;
  const tags: MetaTag[] = [
    { title: input.title },
    { name: "description", content: input.description },
    { property: "og:type", content: input.type ?? "website" },
    { property: "og:title", content: input.title },
    { property: "og:description", content: input.description },
    { property: "og:image", content: imageURL },
    { property: "og:image:type", content: png ? "image/png" : "image/svg+xml" },
    { property: "og:image:width", content: "1200" },
    { property: "og:image:height", content: "630" },
    { name: "twitter:card", content: "summary_large_image" },
    { name: "twitter:title", content: input.title },
    { name: "twitter:description", content: input.description },
    { name: "twitter:image", content: imageURL },
  ];
  if (input.path) {
    tags.push({ property: "og:url", content: `${SITE_URL}${input.path}` });
  }
  return tags;
}

// Canonical URL for the route's head `links`. One canonical per page keeps
// search engines from splitting rank across ?query variants and mirrors.
export function canonicalLink(path: string): { rel: "canonical"; href: string } {
  return { rel: "canonical", href: `${SITE_URL}${path}` };
}

export { SITE_URL };

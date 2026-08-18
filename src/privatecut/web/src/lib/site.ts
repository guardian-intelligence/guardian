// Canonical origin for absolute URLs (canonical link, OG/Twitter cards,
// sitemap, JSON-LD). Compile-time constant pointing at prod: previews carry
// noindex via deployMetaTags(), so every indexable surface resolves here.
export const SITE_URL = "https://rumi.engineering";

export const SITE_TITLE = "PrivateCut — compress video to fit any size limit, no upload";

export const SITE_DESCRIPTION =
  "Video over the size limit? Trim MP4, M4V, MOV, MKV, WebM, Ogg, or MPEG-TS to the best quality that fits, then export MP4 or WebM. Free, no account, and nothing is ever uploaded.";

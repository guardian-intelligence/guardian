// Canonical origin for absolute URLs (canonical link, OG/Twitter cards,
// sitemap, JSON-LD). Compile-time constant pointing at prod: previews carry
// noindex via deployMetaTags(), so every indexable surface resolves here.
export const SITE_URL = "https://rumi.engineering";

export const SITE_TITLE = "PrivateCut — compress video to fit any size limit, no upload";

export const SITE_DESCRIPTION =
  "Video over the size limit? Trim up to a minute, pick a cap — 4, 10, 25, or 100 MB — and get the best quality that fits. MP4, MOV, or WebM in — MP4 out. Free, no account, and nothing is ever uploaded.";

// Resolving an X (Twitter) post link to its downloadable mp4 variants.
//
// The pure parts live here so both the server route (/api/resolve) and the
// client can share them under test. The server never fetches a user-supplied
// URL: it parses the numeric status id out of the pasted link and constructs
// the cdn.syndication.twimg.com URL itself.

export interface XVideoVariant {
  readonly url: string;
  readonly bitrate: number;
  readonly width: number | null;
  readonly height: number | null;
}

export type ResolveResponse =
  | {
      readonly kind: "ok";
      readonly id: string;
      readonly text: string;
      readonly variants: readonly XVideoVariant[];
    }
  | {
      readonly kind: "error";
      readonly code: "bad_url" | "not_found" | "no_video" | "broadcast" | "upstream";
      readonly message: string;
    };

const STATUS_PATH = /^\/(?:[A-Za-z0-9_]{1,15}|i(?:\/web)?)\/status(?:es)?\/(\d{1,20})(?:\/.*)?$/;
const HOSTS = new Set([
  "x.com",
  "www.x.com",
  "twitter.com",
  "www.twitter.com",
  "mobile.twitter.com",
]);

export function parseStatusId(input: string): string | null {
  const trimmed = input.trim();
  if (trimmed === "") return null;
  let url: URL;
  try {
    url = new URL(/^https?:\/\//i.test(trimmed) ? trimmed : `https://${trimmed}`);
  } catch {
    return null;
  }
  if (!HOSTS.has(url.hostname.toLowerCase())) return null;
  return STATUS_PATH.exec(url.pathname)?.[1] ?? null;
}

// The syndication endpoint requires this derived token alongside the id; the
// float precision loss in Number(id) is part of the expected derivation.
export function syndicationToken(id: string): string {
  return ((Number(id) / 1e15) * Math.PI).toString(36).replace(/(0+|\.)/g, "");
}

export function syndicationUrl(id: string): string {
  return `https://cdn.syndication.twimg.com/tweet-result?id=${id}&token=${syndicationToken(id)}`;
}

const VARIANT_DIMS = /\/(\d{2,5})x(\d{2,5})\//;

export function toVariant(raw: {
  readonly content_type?: string;
  readonly url?: string;
  readonly bitrate?: number;
}): XVideoVariant | null {
  if (raw.content_type !== "video/mp4" || typeof raw.url !== "string") return null;
  let host: string;
  try {
    host = new URL(raw.url).hostname;
  } catch {
    return null;
  }
  if (host !== "video.twimg.com") return null;
  const dims = VARIANT_DIMS.exec(raw.url);
  return {
    url: raw.url,
    bitrate: raw.bitrate ?? 0,
    width: dims ? Number(dims[1]) : null,
    height: dims ? Number(dims[2]) : null,
  };
}

// Highest bitrate wins: the engine streams only the ranges the clip needs,
// so source size never argues against quality.
export function pickVariant(variants: readonly XVideoVariant[]): XVideoVariant | null {
  return [...variants].sort((a, b) => b.bitrate - a.bitrate)[0] ?? null;
}

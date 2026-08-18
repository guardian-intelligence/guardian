import { marked } from "marked";
import * as v from "valibot";
import { setResponseHeader } from "@tanstack/react-start/server";
import { inkWrapHtml } from "~/features/letters/ink";
import { LetterFrontmatterSchema } from "./letter-schema";
import type { Letter } from "./letters";

// Runtime letters source. The site reads published letters from Directus and
// renders them at request time; there is no baked copy. Availability is
// layered instead of duplicated: this module serves its last-good fetch
// through any Directus failure, and the edge caches /letters* pages
// (stale-if-error) so readers keep being served even when the origin is down.
//
// DIRECTUS_URL          in-cluster service URL (web.yaml); localhost default
//                       matches a `kubectl port-forward svc/directus 8055:80`
// DIRECTUS_TOKEN        optional bearer token (previewing drafts locally)
// DIRECTUS_INCLUDE_DRAFTS=true
//                       local preview only: also fetch letters without a
//                       summary (requires DIRECTUS_TOKEN; the anonymous role
//                       can only ever see published letters)

const FIELDS = [
  "slug",
  "title",
  "publishedAt",
  "flare",
  "kind",
  "summary",
  "author",
  "authorTitle",
  "description",
  "note",
  "body",
] as const;

// Directus returns null for unset fields; the schema wants them absent.
const ItemSchema = v.record(v.string(), v.nullable(v.string()));

function renderLetter(raw: unknown): Letter | null {
  const item = v.safeParse(ItemSchema, raw);
  if (!item.success) {
    console.error(JSON.stringify({ msg: "letters: non-object item from Directus, skipped" }));
    return null;
  }
  const frontmatter: Record<string, string> = {};
  for (const key of FIELDS) {
    if (key === "body") continue;
    const value = item.output[key];
    if (typeof value === "string" && value !== "") frontmatter[key] = value;
  }
  const parsed = v.safeParse(LetterFrontmatterSchema, frontmatter);
  if (!parsed.success) {
    // A bad Studio edit unpublishes that letter (its page 404s, which the
    // synthetic probes catch) instead of taking the whole shelf down.
    console.error(
      JSON.stringify({
        msg: "letters: invalid letter skipped",
        slug: item.output["slug"] ?? "",
        issues: parsed.issues.map((issue) => issue.message),
      }),
    );
    return null;
  }
  const tokens = marked.lexer(item.output["body"] ?? "");
  const flowTokens = tokens.filter((token) => token.type !== "space");
  const [leadToken, ...continuationTokens] = flowTokens;
  // Every rendered word of the letter wears its own ink (see
  // features/letters/ink.ts). Word indices count from 0 per fragment; the
  // index excerpt re-counts the lead's words from 0 at render time, so both
  // sides of the view transition agree on each word's ink. The full `html`
  // stays unwrapped: it never renders (LetterBody reads lead/continuation;
  // the excerpt fallback strips tags).
  const slug = parsed.output.slug;
  return {
    ...parsed.output,
    bodyHtml: marked.parser(tokens),
    leadHtml: leadToken ? inkWrapHtml(marked.parser([leadToken]), slug) : "",
    continuationHtml:
      continuationTokens.length > 0 ? inkWrapHtml(marked.parser(continuationTokens), slug) : "",
  };
}

async function fetchLetters(): Promise<readonly Letter[]> {
  const base = process.env["DIRECTUS_URL"]?.trim() || "http://127.0.0.1:8055";
  const includeDrafts = process.env["DIRECTUS_INCLUDE_DRAFTS"] === "true";
  const token = process.env["DIRECTUS_TOKEN"]?.trim();
  const url = new URL(`${base}/items/letters`);
  url.searchParams.set("limit", "-1");
  url.searchParams.set("fields", FIELDS.join(","));
  const response = await fetch(url, {
    headers: {
      accept: "application/json",
      ...(token ? { authorization: `Bearer ${token}` } : {}),
    },
    signal: AbortSignal.timeout(10_000),
  });
  if (!response.ok) {
    throw new Error(`letters: Directus responded ${response.status}`);
  }
  const json = (await response.json()) as { data?: unknown[] };
  const letters = (json.data ?? [])
    .map(renderLetter)
    .filter((letter): letter is Letter => letter !== null)
    // Empty summary = draft. The anonymous role is permission-filtered to
    // published letters already; this keeps the gate true regardless of the
    // credential in play.
    .filter((letter) => includeDrafts || letter.summary.trim() !== "");
  return [...letters].sort((a, b) => (a.publishedAt < b.publishedAt ? 1 : -1));
}

const CACHE_TTL_MS = 60_000;

let cache: { readonly letters: readonly Letter[]; readonly at: number } | null = null;
let inflight: Promise<readonly Letter[]> | null = null;

// Stale-while-revalidate, in process. Fresh cache is served as-is; a stale
// cache is served immediately while one refresh runs behind the request; only
// a cold start with Directus down has nothing to serve and fails the request.
export async function publishedLetters(): Promise<readonly Letter[]> {
  if (cache && Date.now() - cache.at < CACHE_TTL_MS) return cache.letters;
  inflight ??= fetchLetters()
    .then((letters) => {
      cache = { letters, at: Date.now() };
      return letters;
    })
    .finally(() => {
      inflight = null;
    });
  if (cache) {
    inflight.catch((error: unknown) => {
      console.error(
        JSON.stringify({ msg: "letters: refresh failed, serving last-good", error: String(error) }),
      );
    });
    return cache.letters;
  }
  return inflight;
}

export async function publishedLetterBySlug(slug: string): Promise<Letter | undefined> {
  return (await publishedLetters()).find((letter) => letter.slug === slug);
}

// Origin half of the edge cache: the Cloudflare cache rule for /letters*
// (guardian-mgmt-edge-policy) runs in respect_origin mode, so this header is
// what makes the pages cacheable at all. stale-if-error keeps cached readers
// served through a full origin outage.
export function setLettersEdgeCacheHeader(): void {
  setResponseHeader(
    "cache-control",
    "public, max-age=60, s-maxage=300, stale-while-revalidate=600, stale-if-error=86400",
  );
}

export function setUncacheableHeader(): void {
  setResponseHeader("cache-control", "no-store");
}

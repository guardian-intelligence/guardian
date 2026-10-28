import { marked } from "marked";
import * as v from "valibot";
import { setResponseHeader } from "@tanstack/react-start/server";
import { inkWrapHtml } from "~/features/letters/ink";
import { LetterFrontmatterSchema } from "./letter-schema";
import type { Letter } from "./letters";
import { allLetterRecords, type LetterDraft } from "./letters-store.server";

// Runtime letters source. The site reads letters from its own store
// (letters-store.server.ts) and renders them at request time; there is no
// baked copy. Availability is layered instead of duplicated: this module
// serves its last-good read through any database failure, and the edge caches
// /letters* pages (stale-if-error) so readers keep being served even when the
// origin is down.
//
// LETTERS_INCLUDE_DRAFTS=true
//                       local preview only: also render draft letters

export function renderLetter(record: LetterDraft): Letter | null {
  const frontmatter: Record<string, string> = {};
  for (const [key, value] of Object.entries(record)) {
    if (key !== "body" && typeof value === "string" && value !== "") frontmatter[key] = value;
  }
  const parsed = v.safeParse(LetterFrontmatterSchema, frontmatter);
  if (!parsed.success) {
    // An invalid row unpublishes that letter (its page 404s, which the
    // synthetic probes catch) instead of taking the whole shelf down.
    console.error(
      JSON.stringify({
        msg: "letters: invalid letter skipped",
        slug: record.slug,
        issues: parsed.issues.map((issue) => issue.message),
      }),
    );
    return null;
  }
  const tokens = marked.lexer(record.body);
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
  const includeDrafts = process.env["LETTERS_INCLUDE_DRAFTS"] === "true";
  const letters = (await allLetterRecords())
    .filter((record) => includeDrafts || record.status === "published")
    .map(renderLetter)
    .filter((letter): letter is Letter => letter !== null);
  return [...letters].sort((a, b) => (a.publishedAt < b.publishedAt ? 1 : -1));
}

const CACHE_TTL_MS = 60_000;

let cache: { readonly letters: readonly Letter[]; readonly at: number } | null = null;
let inflight: Promise<readonly Letter[]> | null = null;

// Stale-while-revalidate, in process. Fresh cache is served as-is; a stale
// cache is served immediately while one refresh runs behind the request; only
// a cold start with the database down has nothing to serve and fails the
// request.
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

// After a save: this replica re-reads at once so the editor's next view is
// the saved letter; the other replicas pick it up within the TTL, the edge
// within its s-maxage. A failed re-read keeps the last-good shelf.
export async function refreshLetters(): Promise<void> {
  try {
    cache = { letters: await fetchLetters(), at: Date.now() };
  } catch (error) {
    console.error(
      JSON.stringify({ msg: "letters: refresh after save failed", error: String(error) }),
    );
  }
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

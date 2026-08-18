import { trace } from "@opentelemetry/api";
import { publishedLetterBySlug } from "~/content/letters.server";
import { LETTER_SLUG_PREFIX, letterOgSpec, OG_CATALOG } from "./catalog";
import { buildOGCard, formatOGError } from "./template";
import type { OGSpec } from "./template";

// Shared OG-card request handler. Both /og/$slug (top-level) and
// /og/letter/$slug (per-letter) call into this with their canonical slug
// already resolved. Centralising avoids drift between the two routes when
// caching, tracing, or error semantics evolve.

const TRACER = trace.getTracer("guardian/company-og", "0.1.0");

async function ogSpecFor(slug: string): Promise<OGSpec | undefined> {
  if (slug.startsWith(LETTER_SLUG_PREFIX)) {
    const letter = await publishedLetterBySlug(slug.slice(LETTER_SLUG_PREFIX.length));
    return letter ? letterOgSpec(letter) : undefined;
  }
  return OG_CATALOG[slug];
}

export async function ogHeadResponse(slug: string): Promise<Response> {
  const cleaned = slug.replace(/\.svg$|\.png$/, "");
  const spec = await ogSpecFor(cleaned);
  if (!spec) {
    return new Response(null, { status: 404, headers: { "content-type": "text/plain" } });
  }
  return new Response(null, {
    status: 200,
    headers: {
      "content-type": "image/svg+xml",
      "cache-control": "public, max-age=600, s-maxage=600",
    },
  });
}

export async function ogGetResponse(slug: string): Promise<Response> {
  const cleaned = slug.replace(/\.svg$|\.png$/, "");
  const spec = await ogSpecFor(cleaned);
  return TRACER.startActiveSpan("company.og.render", (span) => {
    span.setAttribute("og.slug", cleaned);
    try {
      if (!spec) {
        span.setAttribute("og.voice_pass", "false");
        span.setAttribute("og.error", "slug_not_found");
        span.setStatus({ code: 2, message: "slug not found" });
        return new Response(`og slug not found: ${cleaned}`, {
          status: 404,
          headers: { "content-type": "text/plain" },
        });
      }
      const result = buildOGCard(spec);
      if (!result.ok) {
        span.setAttribute("og.voice_pass", "false");
        span.setAttribute("og.error_kind", result.error.kind);
        span.setStatus({ code: 2, message: formatOGError(result.error) });
        return new Response(`og build failed: ${formatOGError(result.error)}`, {
          status: 500,
          headers: { "content-type": "text/plain" },
        });
      }
      span.setAttribute("og.voice_pass", "true");
      span.setAttribute("og.content_hash", result.contentHash);
      span.setAttribute("og.bytes", String(new TextEncoder().encode(result.svg).length));
      return new Response(result.svg, {
        status: 200,
        headers: {
          "content-type": "image/svg+xml",
          "cache-control": "public, max-age=600, s-maxage=600",
        },
      });
    } finally {
      span.end();
    }
  });
}

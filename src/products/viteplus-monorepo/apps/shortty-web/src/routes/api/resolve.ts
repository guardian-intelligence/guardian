import { createFileRoute } from "@tanstack/react-router";
import { SpanKind, SpanStatusCode, type Span } from "@opentelemetry/api";
import { childContext, exportedTraceId, ready, tracer } from "~/lib/telemetry/otel-node";
import {
  parseStatusId,
  syndicationUrl,
  toVariant,
  type ResolveResponse,
  type XVideoVariant,
} from "~/lib/x-link";

// Resolves a pasted X post link to its mp4 variant ladder. The video bytes
// themselves never touch this server: video.twimg.com serves them to the
// browser with permissive CORS, while the syndication metadata endpoint is
// CORS-locked to platform.twitter.com — hence this one server-side hop.
// Every request is traced and answers with x-guardian-trace-id, which the
// client attaches to its shortty.link_* analytics events — the join key
// from a user-visible failure to the server spans in
// guardian_analytics.otel_traces.

const headers = { "cache-control": "no-store", "content-type": "application/json" } as const;

function json(body: ResolveResponse, status: number): Response {
  return new Response(JSON.stringify(body), { status, headers });
}

type ErrorCode = Exclude<ResolveResponse, { kind: "ok" }>["code"];

interface Resolved {
  readonly response: Response;
  readonly code: ErrorCode | "ok";
}

function fail(code: ErrorCode, message: string, status: number): Resolved {
  return { response: json({ kind: "error", code, message }, status), code };
}

interface SyndicationMedia {
  readonly type?: string;
  readonly video_info?: {
    readonly variants?: readonly { content_type?: string; url?: string; bitrate?: number }[];
  };
}

interface SyndicationTweet {
  readonly __typename?: string;
  readonly text?: string;
  readonly mediaDetails?: readonly SyndicationMedia[];
  readonly card?: { readonly name?: string };
}

async function resolve(request: Request, span: Span): Promise<Resolved> {
  const raw = new URL(request.url).searchParams.get("url") ?? "";
  const id = parseStatusId(raw);
  if (id === null) {
    return fail("bad_url", "That doesn't look like a link to an X post.", 400);
  }
  span.setAttribute("x.status_id", id);

  const fetchSpan = tracer().startSpan(
    "x.syndication.fetch",
    { kind: SpanKind.CLIENT, attributes: { "server.address": "cdn.syndication.twimg.com" } },
    childContext(span),
  );
  let upstream: Response;
  try {
    upstream = await fetch(syndicationUrl(id), {
      headers: { "user-agent": "Mozilla/5.0 (compatible; shortty/1.0)" },
      signal: AbortSignal.timeout(10_000),
    });
    fetchSpan.setAttribute("http.response.status_code", upstream.status);
  } catch (error) {
    fetchSpan.setStatus({
      code: SpanStatusCode.ERROR,
      message: error instanceof Error ? error.message : "fetch failed",
    });
    return fail("upstream", "Couldn't reach X to look up that post. Try again.", 502);
  } finally {
    fetchSpan.end();
  }
  if (upstream.status === 404) {
    return fail("not_found", "That post doesn't exist or isn't public.", 404);
  }
  if (!upstream.ok) {
    return fail("upstream", "X returned an error looking up that post. Try again.", 502);
  }

  let tweet: SyndicationTweet;
  try {
    tweet = (await upstream.json()) as SyndicationTweet;
  } catch {
    return fail("upstream", "X returned an unreadable response. Try again.", 502);
  }
  if (tweet.__typename === "TweetTombstone") {
    // The syndication API tombstones more than deletions: X also hides
    // age-restricted and sensitive-flagged posts from logged-out viewers,
    // so a post the user can see in their own session may still land here.
    return fail(
      "not_found",
      "X won't show that post to logged-out viewers — it may be age-restricted, protected, or deleted.",
      404,
    );
  }

  const variants: XVideoVariant[] = [];
  for (const media of tweet.mediaDetails ?? []) {
    if (media.type !== "video" && media.type !== "animated_gif") continue;
    for (const v of media.video_info?.variants ?? []) {
      const variant = toVariant(v);
      if (variant) variants.push(variant);
    }
    if (variants.length > 0) break;
  }

  if (variants.length === 0) {
    if (tweet.card?.name?.endsWith(":broadcast")) {
      return fail("broadcast", "That's a live broadcast — those have no downloadable video.", 422);
    }
    return fail("no_video", "That post doesn't have a video.", 422);
  }

  return { response: json({ kind: "ok", id, text: tweet.text ?? "", variants }, 200), code: "ok" };
}

export const Route = createFileRoute("/api/resolve")({
  server: {
    handlers: {
      GET: async ({ request }) => {
        await ready;
        const span = tracer().startSpan("shortty.resolve", { kind: SpanKind.SERVER });
        try {
          const { response, code } = await resolve(request, span);
          span.setAttribute("shortty.resolve.code", code);
          span.setAttribute("http.response.status_code", response.status);
          if (response.status >= 500) span.setStatus({ code: SpanStatusCode.ERROR });
          const traceId = exportedTraceId(span);
          if (traceId !== "") response.headers.set("x-guardian-trace-id", traceId);
          return response;
        } finally {
          span.end();
        }
      },
    },
  },
});

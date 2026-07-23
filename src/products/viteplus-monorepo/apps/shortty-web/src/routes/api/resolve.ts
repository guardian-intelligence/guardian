import { createFileRoute } from "@tanstack/react-router";
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

const headers = { "cache-control": "no-store", "content-type": "application/json" } as const;

function json(body: ResolveResponse, status: number): Response {
  return new Response(JSON.stringify(body), { status, headers });
}

type ErrorCode = Exclude<ResolveResponse, { kind: "ok" }>["code"];

function fail(code: ErrorCode, message: string, status: number): Response {
  return json({ kind: "error", code, message }, status);
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

export const Route = createFileRoute("/api/resolve")({
  server: {
    handlers: {
      GET: async ({ request }) => {
        const raw = new URL(request.url).searchParams.get("url") ?? "";
        const id = parseStatusId(raw);
        if (id === null) {
          return fail("bad_url", "That doesn't look like a link to an X post.", 400);
        }

        let upstream: Response;
        try {
          upstream = await fetch(syndicationUrl(id), {
            headers: { "user-agent": "Mozilla/5.0 (compatible; shortty/1.0)" },
            signal: AbortSignal.timeout(10_000),
          });
        } catch {
          return fail("upstream", "Couldn't reach X to look up that post. Try again.", 502);
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
          return fail(
            "not_found",
            "That post is unavailable — it may be protected or deleted.",
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
            return fail(
              "broadcast",
              "That's a live broadcast — those have no downloadable video.",
              422,
            );
          }
          return fail("no_video", "That post doesn't have a video.", 422);
        }

        return json({ kind: "ok", id, text: tweet.text ?? "", variants }, 200);
      },
    },
  },
});

import { pickVariant, type ResolveResponse } from "~/lib/x-link";

// Client side of the X-link import: resolve via our same-origin route, then
// hand the engine the CDN URL. video.twimg.com answers ranged CORS requests
// (it echoes our Origin and allows the Range header), so the engine streams
// only the bytes the probe and the selected clip actually need — the full
// video is never downloaded and never touches our servers.
//
// The route answers with x-guardian-trace-id; it rides along on both
// outcomes so link_resolved/link_failed events can name the exact server
// trace that handled them.

export class ResolveError extends Error {
  constructor(
    readonly code: string,
    message: string,
    readonly traceId: string = "",
  ) {
    super(message);
  }
}

export interface ResolvedVideo {
  readonly source: { readonly url: string; readonly name: string };
  readonly traceId: string;
}

export async function resolveXVideo(link: string): Promise<ResolvedVideo> {
  let payload: ResolveResponse;
  let traceId = "";
  try {
    const res = await fetch(`/api/resolve?url=${encodeURIComponent(link)}`);
    traceId = res.headers.get("x-guardian-trace-id") ?? "";
    payload = (await res.json()) as ResolveResponse;
  } catch {
    throw new ResolveError(
      "network",
      "Couldn't reach the resolver. Check your connection.",
      traceId,
    );
  }
  if (payload.kind === "error") throw new ResolveError(payload.code, payload.message, traceId);

  const variant = pickVariant(payload.variants);
  if (!variant) throw new ResolveError("no_video", "That post doesn't have a video.", traceId);
  return { source: { url: variant.url, name: `x-${payload.id}.mp4` }, traceId };
}

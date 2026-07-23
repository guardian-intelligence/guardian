import { pickVariant, type ResolveResponse } from "~/lib/x-link";

// Client side of the X-link import: resolve via our same-origin route, then
// hand the engine the CDN URL. video.twimg.com answers ranged CORS requests
// (it echoes our Origin and allows the Range header), so the engine streams
// only the bytes the probe and the selected clip actually need — the full
// video is never downloaded and never touches our servers.

export class ResolveError extends Error {
  constructor(
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

export async function resolveXVideo(link: string): Promise<{ url: string; name: string }> {
  let payload: ResolveResponse;
  try {
    const res = await fetch(`/api/resolve?url=${encodeURIComponent(link)}`);
    payload = (await res.json()) as ResolveResponse;
  } catch {
    throw new ResolveError("network", "Couldn't reach the resolver. Check your connection.");
  }
  if (payload.kind === "error") throw new ResolveError(payload.code, payload.message);

  const variant = pickVariant(payload.variants);
  if (!variant) throw new ResolveError("no_video", "That post doesn't have a video.");
  return { url: variant.url, name: `x-${payload.id}.mp4` };
}

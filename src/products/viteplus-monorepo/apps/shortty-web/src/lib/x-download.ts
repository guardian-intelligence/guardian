import { pickVariant, type ResolveResponse } from "~/lib/x-link";

// Client side of the X-link import: resolve via our same-origin route, then
// stream the mp4 straight from video.twimg.com (it echoes our Origin in
// access-control-allow-origin, so the bytes never touch our servers).

export class ResolveError extends Error {
  constructor(
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

export async function importFromXLink(
  link: string,
  onProgress: (fraction: number | null) => void,
): Promise<File> {
  let payload: ResolveResponse;
  try {
    const res = await fetch(`/api/resolve?url=${encodeURIComponent(link)}`);
    payload = (await res.json()) as ResolveResponse;
  } catch {
    throw new ResolveError("network", "Couldn't reach the resolver. Check your connection.");
  }
  if (payload.kind === "error") throw new ResolveError(payload.code, payload.message);

  const variant = pickVariant(payload.variants, payload.durationS);
  if (!variant) throw new ResolveError("no_video", "That post doesn't have a video.");

  const res = await fetch(variant.url);
  if (!res.ok || !res.body) {
    throw new ResolveError("download", "The video download failed. Try again.");
  }
  const total = Number(res.headers.get("content-length")) || null;
  const reader = res.body.getReader();
  const chunks: Uint8Array[] = [];
  let received = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    chunks.push(value);
    received += value.byteLength;
    onProgress(total ? Math.min(received / total, 1) : null);
  }
  return new File(chunks as BlobPart[], `x-${payload.id}.mp4`, { type: "video/mp4" });
}

import type { VideoDecodeMode } from "./types";

export function resolveVideoDecodeMode(
  canDecodeWithWebCodecs: boolean,
  codec: string | null,
  isLocalFile: boolean,
): VideoDecodeMode | null {
  if (canDecodeWithWebCodecs) return "webcodecs";
  if (isLocalFile && codec === "hevc") return "media-element";
  return null;
}

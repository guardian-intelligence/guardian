import type { OutputContainer } from "./types";

export const INPUT_CONTAINER_LABEL = "MP4, M4V, MOV, MKV, WebM, Ogg, or MPEG-TS";
export const INPUT_CODEC_LABEL = "H.264, H.265, VP8, VP9, or AV1";

// The picker hint matches the video-bearing subset of Mediabunny's input
// formats. Extensions are included because several operating systems do not
// assign useful MIME types to Matroska and MPEG-TS files.
export const INPUT_FILE_ACCEPT = [
  ".mp4",
  ".m4v",
  ".mov",
  ".mkv",
  ".webm",
  ".ogg",
  ".ogv",
  ".ts",
  ".mts",
  ".m2ts",
  "video/mp4",
  "video/x-m4v",
  "video/quicktime",
  "video/x-matroska",
  "video/webm",
  "video/ogg",
  "application/ogg",
  "video/mp2t",
].join(",");

export interface OutputContainerInfo {
  readonly label: string;
  readonly extension: ".mp4" | ".webm";
  readonly mimeType: "video/mp4" | "video/webm";
}

export const OUTPUT_CONTAINERS: readonly OutputContainer[] = ["mp4", "webm"];

export const OUTPUT_CONTAINER_INFO: Record<OutputContainer, OutputContainerInfo> = {
  mp4: {
    label: "MP4",
    extension: ".mp4",
    mimeType: "video/mp4",
  },
  webm: {
    label: "WebM",
    extension: ".webm",
    mimeType: "video/webm",
  },
};

export function canRemuxCodecs(
  format: OutputContainer,
  videoCodec: string,
  audioCodec: string | null,
): boolean {
  if (format === "mp4") {
    return videoCodec === "avc" && (audioCodec === null || audioCodec === "aac");
  }
  return (
    ["vp8", "vp9", "av1"].includes(videoCodec) &&
    (audioCodec === null || ["opus", "vorbis"].includes(audioCodec))
  );
}

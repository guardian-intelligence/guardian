import {
  type AudioCodec,
  getFirstEncodableAudioCodec,
  getFirstEncodableVideoCodec,
  Mp4OutputFormat,
  type OutputFormat,
  type VideoCodec,
  WebMOutputFormat,
} from "mediabunny";
import { OUTPUT_CONTAINERS } from "./formats";
import type { VideoCodecChoice } from "./ladder";
import type { AudioCodecChoice, OutputContainer, OutputProfile } from "./types";

const VIDEO_CODECS: Record<OutputContainer, VideoCodec[]> = {
  mp4: ["avc"],
  webm: ["vp9", "vp8", "av1"],
};

const AUDIO_CODECS: Record<OutputContainer, AudioCodec[]> = {
  mp4: ["aac"],
  webm: ["opus", "vorbis"],
};

export function createOutputFormat(format: OutputContainer): OutputFormat {
  return format === "mp4"
    ? new Mp4OutputFormat({ fastStart: "in-memory" })
    : new WebMOutputFormat();
}

export async function resolveOutputProfile(
  format: OutputContainer,
  hasAudio: boolean,
): Promise<OutputProfile | null> {
  const [videoCodec, audioCodec] = await Promise.all([
    getFirstEncodableVideoCodec(VIDEO_CODECS[format]),
    hasAudio ? getFirstEncodableAudioCodec(AUDIO_CODECS[format]) : Promise.resolve(null),
  ]);
  if (videoCodec === null || (hasAudio && audioCodec === null)) return null;
  return {
    format,
    videoCodec: videoCodec as VideoCodecChoice,
    audioCodec: audioCodec as AudioCodecChoice | null,
  };
}

export async function resolveOutputProfiles(hasAudio: boolean): Promise<readonly OutputProfile[]> {
  const profiles = await Promise.all(
    OUTPUT_CONTAINERS.map((format) => resolveOutputProfile(format, hasAudio)),
  );
  return profiles.filter((profile): profile is OutputProfile => profile !== null);
}

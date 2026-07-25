import {
  ALL_FORMATS,
  BlobSource,
  EncodedPacketSink,
  Input,
  UrlSource,
  type InputAudioTrack,
  type InputVideoTrack,
} from "mediabunny";
import type { MediaSource, ProbeSummary } from "./types";

export interface OpenedInput {
  readonly input: Input;
  readonly videoTrack: InputVideoTrack;
  readonly audioTrack: InputAudioTrack | null;
  readonly summary: ProbeSummary;
}

// Opens the session source lazily — BlobSource slices a dropped file on
// demand, UrlSource range-requests a remote mp4 — so a multi-GB source never
// fully enters memory. Builds the session summary the editor runs on:
// dimensions, frame rate, bitrate, and the keyframe map for the timeline's
// snap ticks and the remux fast path.
export async function openInput(source: MediaSource): Promise<OpenedInput> {
  const input = new Input({
    formats: ALL_FORMATS,
    source:
      source instanceof File
        ? new BlobSource(source)
        : // video.twimg.com hotlink-blocks any third-party Referer with a
          // 403 (even the origin-only form browsers send by default), and a
          // worker's fetches are not covered by the document's referrer
          // policy — it must be set per-request here.
          new UrlSource(source.url, { requestInit: { referrerPolicy: "no-referrer" } }),
  });
  const videoTrack = await input.getPrimaryVideoTrack();
  if (videoTrack === null) {
    throw new Error("This file has no video track we can read.");
  }
  if (!(await videoTrack.canDecode())) {
    throw new Error("This browser cannot decode this video's codec.");
  }
  const audioTrack = await input.getPrimaryAudioTrack();
  const durationS = await input.computeDuration();
  const stats = await videoTrack.computePacketStats(240);
  const keyframesS = await scanKeyframes(videoTrack);
  const summary: ProbeSummary = {
    durationS,
    video: {
      width: await videoTrack.getDisplayWidth(),
      height: await videoTrack.getDisplayHeight(),
      frameRate: stats.averagePacketRate,
      codec: (await videoTrack.getCodec()) ?? "unknown",
      bitsPerSecond: stats.averageBitrate,
    },
    hasAudio: audioTrack !== null,
    keyframesS,
  };
  return { input, videoTrack, audioTrack, summary };
}

// Metadata-only walk: timestamps and sizes come from the container's sample
// tables, so this touches no media bytes.
async function scanKeyframes(videoTrack: InputVideoTrack): Promise<number[]> {
  const sink = new EncodedPacketSink(videoTrack);
  const keyframes: number[] = [];
  for await (const packet of sink.packets(undefined, undefined, { metadataOnly: true })) {
    if (packet.type === "key") keyframes.push(packet.timestamp);
  }
  keyframes.sort((a, b) => a - b);
  return keyframes;
}

// Exact byte cost of stream-copying the video packets in [startS, endS):
// drives remux eligibility. Metadata-only, so it is cheap even on big files.
export async function measureVideoBytes(
  videoTrack: InputVideoTrack,
  startS: number,
  endS: number,
): Promise<number> {
  const sink = new EncodedPacketSink(videoTrack);
  const first = await sink.getPacket(startS, { metadataOnly: true });
  if (first === null) return 0;
  let total = 0;
  for await (const packet of sink.packets(first, undefined, { metadataOnly: true })) {
    if (packet.timestamp >= endS) break;
    total += packet.byteLength;
  }
  return total;
}

export async function measureAudioBytes(
  audioTrack: InputAudioTrack,
  startS: number,
  endS: number,
): Promise<number> {
  const sink = new EncodedPacketSink(audioTrack);
  const first = await sink.getPacket(startS, { metadataOnly: true });
  if (first === null) return 0;
  let total = 0;
  for await (const packet of sink.packets(first, undefined, { metadataOnly: true })) {
    if (packet.timestamp >= endS) break;
    total += packet.byteLength;
  }
  return total;
}

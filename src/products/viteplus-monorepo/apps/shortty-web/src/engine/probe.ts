import {
  ALL_FORMATS,
  BlobSource,
  EncodedPacketSink,
  Input,
  type InputAudioTrack,
  type InputVideoTrack,
} from "mediabunny";
import type { ProbeSummary } from "./types";

export interface OpenedInput {
  readonly input: Input;
  readonly videoTrack: InputVideoTrack;
  readonly audioTrack: InputAudioTrack | null;
  readonly summary: ProbeSummary;
}

// Opens the dropped file lazily (BlobSource slices on demand — a multi-GB
// file never fully enters memory) and builds the session summary the editor
// runs on: dimensions, frame rate, bitrate, and the keyframe map for the
// timeline's snap ticks and the remux fast path.
export async function openInput(file: File): Promise<OpenedInput> {
  const input = new Input({ formats: ALL_FORMATS, source: new BlobSource(file) });
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

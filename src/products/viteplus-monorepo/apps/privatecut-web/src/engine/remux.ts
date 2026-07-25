import {
  BufferTarget,
  EncodedAudioPacketSource,
  EncodedPacketSink,
  EncodedVideoPacketSource,
  Mp4OutputFormat,
  Output,
} from "mediabunny";
import { estimateContainerBytes } from "./budget";
import { SIZE_LIMIT_BYTES } from "./limits";
import type { OpenedInput } from "./probe";
import { measureAudioBytes, measureVideoBytes } from "./probe";
import type { SelectionRange } from "./types";

const KEYFRAME_TOLERANCE_S = 0.05;

export interface RemuxPlan {
  readonly startS: number;
  readonly endS: number;
  readonly estimatedBytes: number;
}

// The fast path: when the selection starts on a keyframe and the
// stream-copied segment fits the limit, the output is the original bytes —
// no decode, no encode, no generation loss. Returns null when ineligible.
export async function planRemux(
  opened: OpenedInput,
  selection: SelectionRange,
): Promise<RemuxPlan | null> {
  const { summary } = opened;
  const onKeyframe = summary.keyframesS.some(
    (t) => Math.abs(t - selection.startS) <= KEYFRAME_TOLERANCE_S,
  );
  if (!onKeyframe) return null;
  const durationS = selection.endS - selection.startS;
  const videoBytes = await measureVideoBytes(opened.videoTrack, selection.startS, selection.endS);
  if (videoBytes === 0) return null;
  const audioBytes = opened.audioTrack
    ? await measureAudioBytes(opened.audioTrack, selection.startS, selection.endS)
    : 0;
  const containerBytes = estimateContainerBytes(
    durationS,
    summary.video.frameRate,
    opened.audioTrack !== null,
  );
  const estimatedBytes = videoBytes + audioBytes + containerBytes;
  // The estimate gates only whether to ATTEMPT the copy; acceptance is the
  // measured finalized size below, like every other path.
  if (estimatedBytes > SIZE_LIMIT_BYTES * 0.98) return null;
  return { startS: selection.startS, endS: selection.endS, estimatedBytes };
}

export interface RemuxOutcome {
  readonly buffer: ArrayBuffer;
  readonly bytes: number;
}

export async function executeRemux(
  opened: OpenedInput,
  plan: RemuxPlan,
): Promise<RemuxOutcome | null> {
  const videoCodec = await opened.videoTrack.getCodec();
  if (videoCodec === null) return null;
  const target = new BufferTarget();
  const output = new Output({
    format: new Mp4OutputFormat({ fastStart: "in-memory" }),
    target,
  });

  const videoSource = new EncodedVideoPacketSource(videoCodec);
  output.addVideoTrack(videoSource, { rotation: await opened.videoTrack.getRotation() });

  let audioSource: EncodedAudioPacketSource | null = null;
  const audioCodec = opened.audioTrack ? await opened.audioTrack.getCodec() : null;
  if (opened.audioTrack && audioCodec !== null) {
    audioSource = new EncodedAudioPacketSource(audioCodec);
    output.addAudioTrack(audioSource);
  }

  await output.start();

  const videoConfig = await opened.videoTrack.getDecoderConfig();
  const videoSink = new EncodedPacketSink(opened.videoTrack);
  const firstVideo = await videoSink.getKeyPacket(plan.startS, { verifyKeyPackets: true });
  if (firstVideo === null) {
    await output.cancel();
    return null;
  }
  // Re-base on the first copied packet, not the requested start: the
  // keyframe tolerance means they can differ by a few centiseconds, and a
  // negative first video timestamp desyncs A/V against the zero-clamped
  // audio track.
  const baseS = firstVideo.timestamp;
  let firstAdd = true;
  for await (const packet of videoSink.packets(firstVideo)) {
    if (packet.timestamp >= plan.endS) break;
    const shifted = packet.clone({ timestamp: packet.timestamp - baseS });
    await videoSource.add(
      shifted,
      firstAdd && videoConfig !== null ? { decoderConfig: videoConfig } : undefined,
    );
    firstAdd = false;
  }

  if (opened.audioTrack && audioSource !== null) {
    const audioConfig = await opened.audioTrack.getDecoderConfig();
    const audioSink = new EncodedPacketSink(opened.audioTrack);
    const firstAudio = await audioSink.getPacket(baseS);
    if (firstAudio !== null) {
      let firstAudioAdd = true;
      for await (const packet of audioSink.packets(firstAudio)) {
        if (packet.timestamp >= plan.endS) break;
        const shifted = packet.clone({ timestamp: Math.max(packet.timestamp - baseS, 0) });
        await audioSource.add(
          shifted,
          firstAudioAdd && audioConfig !== null ? { decoderConfig: audioConfig } : undefined,
        );
        firstAudioAdd = false;
      }
    }
  }

  await output.finalize();
  const buffer = target.buffer;
  if (buffer === null) return null;
  return { buffer, bytes: buffer.byteLength };
}

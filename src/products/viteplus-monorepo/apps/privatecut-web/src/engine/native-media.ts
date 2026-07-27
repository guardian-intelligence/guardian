import { planBudget } from "./budget";
import { calibratedMargin, recordCalibration } from "./calibration";
import { converge, firstPassBitrate } from "./convergence";
import { OUTPUT_CONTAINER_INFO } from "./formats";
import { planFrame } from "./ladder";
import { MAX_SELECTION_SECONDS } from "./limits";
import type {
  EncodeOutcome,
  OutputContainer,
  OutputProfile,
  ProbeSummary,
  SelectionRange,
} from "./types";

export interface NativeThumbnail {
  readonly index: number;
  readonly timestampS: number;
  readonly bitmap: ImageBitmap;
}

export interface NativeEncodeResult {
  readonly blob: Blob;
  readonly outcome: EncodeOutcome;
}

interface OpenNativeVideo {
  readonly video: HTMLVideoElement;
  readonly dispose: () => void;
}

type CapturableVideo = HTMLVideoElement & {
  captureStream(): MediaStream;
};

type FrameCallbackVideo = HTMLVideoElement & {
  requestVideoFrameCallback(
    callback: (now: DOMHighResTimeStamp, metadata: { mediaTime: number }) => void,
  ): number;
  cancelVideoFrameCallback(handle: number): void;
};

const LOAD_TIMEOUT_MS = 20_000;

export function recorderMimeCandidates(
  profile: OutputProfile,
  includeAudio: boolean,
): readonly string[] {
  if (profile.format === "mp4") {
    return includeAudio
      ? ["video/mp4;codecs=avc1.42001f,mp4a.40.2", "video/mp4;codecs=avc1,mp4a.40.2"]
      : ["video/mp4;codecs=avc1.42001f", "video/mp4;codecs=avc1"];
  }
  const videoCodec = profile.videoCodec === "av1" ? "av1" : profile.videoCodec;
  return includeAudio && profile.audioCodec !== null
    ? [`video/webm;codecs=${videoCodec},${profile.audioCodec}`]
    : [`video/webm;codecs=${videoCodec}`];
}

export function selectRecorderMimeType(
  profile: OutputProfile,
  includeAudio: boolean,
  isSupported: (mimeType: string) => boolean = MediaRecorder.isTypeSupported,
): string | null {
  return recorderMimeCandidates(profile, includeAudio).find(isSupported) ?? null;
}

export async function assertNativePlayback(file: File): Promise<void> {
  const opened = await openNativeVideo(file);
  opened.dispose();
}

export async function createNativeThumbnails(
  file: File,
  durationS: number,
  count: number,
  height: number,
  onThumbnail: (thumbnail: NativeThumbnail) => void,
): Promise<void> {
  const opened = await openNativeVideo(file);
  try {
    const { video } = opened;
    const canvas = document.createElement("canvas");
    canvas.height = height;
    canvas.width = Math.max(1, Math.round(height * (video.videoWidth / video.videoHeight)));
    const context = canvas.getContext("2d");
    if (context === null) throw new Error("This browser cannot create video thumbnails.");

    for (let index = 0; index < count; index += 1) {
      const timestampS = (durationS * (index + 0.5)) / count;
      await seekVideo(video, timestampS);
      context.drawImage(video, 0, 0, canvas.width, canvas.height);
      onThumbnail({
        index,
        timestampS: video.currentTime,
        bitmap: await createImageBitmap(canvas),
      });
    }
  } finally {
    opened.dispose();
  }
}

export async function nativeTranscode(input: {
  readonly file: File;
  readonly summary: ProbeSummary;
  readonly selection: SelectionRange;
  readonly limitBytes: number;
  readonly outputFormat: OutputContainer;
  readonly onProgress: (pass: number, fraction: number) => void;
}): Promise<NativeEncodeResult> {
  const { file, summary, limitBytes, outputFormat, onProgress } = input;
  const selection = {
    startS: Math.max(input.selection.startS, 0),
    endS: Math.min(input.selection.endS, summary.durationS),
  };
  const durationS = selection.endS - selection.startS;
  if (durationS <= 0) throw new Error("Empty selection.");
  if (durationS > MAX_SELECTION_SECONDS + 0.001) {
    throw new Error(`Selections are capped at ${MAX_SELECTION_SECONDS} seconds.`);
  }

  const profile = summary.outputProfiles.find((candidate) => candidate.format === outputFormat);
  if (profile === undefined) {
    throw new Error(`This browser cannot encode ${OUTPUT_CONTAINER_INFO[outputFormat].label}.`);
  }
  const budget = planBudget({
    durationS,
    frameRate: summary.video.frameRate,
    sourceHasAudio: summary.hasAudio,
    sourceVideoBitsPerSecond: summary.video.bitsPerSecond,
    limitBytes,
  });
  const frame = planFrame({
    sourceWidth: summary.video.width,
    sourceHeight: summary.video.height,
    sourceFrameRate: summary.video.frameRate,
    videoBitsPerSecond: budget.videoBitsPerSecond,
    codec: profile.videoCodec,
  });
  const margin = calibratedMargin(profile.videoCodec, frame.height);
  const startedAt = performance.now();
  const result = await converge<Blob>({
    limitBytes,
    reservedBytes: budget.containerBytes + budget.audioBytes,
    initialVideoBitsPerSecond: budget.videoBitsPerSecond,
    maxVideoBitsPerSecond: summary.video.bitsPerSecond,
    safetyMargin: margin,
    encode: async (videoBitsPerSecond, pass) => {
      const blob = await recordNativePass({
        file,
        selection,
        frame,
        profile,
        audioBitsPerSecond: budget.audio?.bitrate ?? null,
        videoBitsPerSecond,
        onProgress: (fraction) => onProgress(pass, fraction),
      });
      return { artifact: blob, bytes: blob.size };
    },
  });

  const firstPass = result.passes[0];
  if (firstPass !== undefined) {
    const requestedBits = firstPassBitrate({
      initialVideoBitsPerSecond: budget.videoBitsPerSecond,
      safetyMargin: margin,
      maxVideoBitsPerSecond: summary.video.bitsPerSecond,
    });
    const actualVideoBits = Math.max(
      (firstPass.bytes - budget.containerBytes - budget.audioBytes) * 8,
      1,
    );
    recordCalibration(
      profile.videoCodec,
      frame.height,
      margin,
      requestedBits * durationS,
      actualVideoBits,
    );
  }

  if (result.bytes > limitBytes) {
    throw new Error("Internal error: output exceeded the size cap.");
  }
  return {
    blob: result.artifact,
    outcome: {
      mode: "transcode",
      outputFormat,
      bytes: result.bytes,
      limitBytes,
      utilization: result.utilization,
      durationS,
      width: frame.width,
      height: frame.height,
      frameRate: frame.frameRate,
      codec: profile.videoCodec,
      passes: result.passes,
      wallMs: performance.now() - startedAt,
    },
  };
}

async function openNativeVideo(file: File): Promise<OpenNativeVideo> {
  const video = document.createElement("video");
  const url = URL.createObjectURL(file);
  video.preload = "auto";
  video.muted = true;
  video.playsInline = true;
  video.src = url;
  Object.assign(video.style, {
    height: "1px",
    left: "-10px",
    opacity: "0",
    pointerEvents: "none",
    position: "fixed",
    top: "-10px",
    width: "1px",
  });
  document.body.append(video);

  try {
    await new Promise<void>((resolve, reject) => {
      const timeout = window.setTimeout(() => {
        cleanup();
        reject(new Error("Timed out while opening this HEVC MOV file."));
      }, LOAD_TIMEOUT_MS);
      const cleanup = () => {
        window.clearTimeout(timeout);
        video.removeEventListener("canplay", loaded);
        video.removeEventListener("error", failed);
      };
      const loaded = () => {
        cleanup();
        if (video.videoWidth === 0 || video.videoHeight === 0) {
          reject(new Error("This browser cannot display this HEVC MOV file."));
          return;
        }
        resolve();
      };
      const failed = () => {
        cleanup();
        reject(new Error("This browser cannot display this HEVC MOV file."));
      };
      video.addEventListener("canplay", loaded);
      video.addEventListener("error", failed);
      video.load();
    });
  } catch (error) {
    video.remove();
    URL.revokeObjectURL(url);
    throw error;
  }

  return {
    video,
    dispose: () => {
      video.pause();
      video.removeAttribute("src");
      video.load();
      video.remove();
      URL.revokeObjectURL(url);
    },
  };
}

async function seekVideo(video: HTMLVideoElement, timestampS: number): Promise<void> {
  const target = Math.min(Math.max(timestampS, 0), Math.max(video.duration - 0.001, 0));
  if (Math.abs(video.currentTime - target) <= 0.005 && video.readyState >= 2) return;
  await new Promise<void>((resolve, reject) => {
    const timeout = window.setTimeout(() => {
      cleanup();
      reject(new Error("Timed out while seeking this HEVC MOV file."));
    }, LOAD_TIMEOUT_MS);
    const cleanup = () => {
      window.clearTimeout(timeout);
      video.removeEventListener("seeked", seeked);
      video.removeEventListener("error", failed);
    };
    const seeked = () => {
      cleanup();
      resolve();
    };
    const failed = () => {
      cleanup();
      reject(new Error("This browser could not seek this HEVC MOV file."));
    };
    video.addEventListener("seeked", seeked);
    video.addEventListener("error", failed);
    video.currentTime = target;
  });
}

async function recordNativePass(input: {
  readonly file: File;
  readonly selection: SelectionRange;
  readonly frame: { readonly width: number; readonly height: number; readonly frameRate: number };
  readonly profile: OutputProfile;
  readonly audioBitsPerSecond: number | null;
  readonly videoBitsPerSecond: number;
  readonly onProgress: (fraction: number) => void;
}): Promise<Blob> {
  const { file, selection, frame, profile, audioBitsPerSecond, videoBitsPerSecond, onProgress } =
    input;
  const mimeType = selectRecorderMimeType(profile, audioBitsPerSecond !== null);
  if (mimeType === null) {
    throw new Error(
      `This browser cannot record ${OUTPUT_CONTAINER_INFO[profile.format].label} from HEVC video.`,
    );
  }

  const opened = await openNativeVideo(file);
  const { video } = opened;
  let canvasStream: MediaStream | null = null;
  let sourceStream: MediaStream | null = null;
  try {
    await seekVideo(video, selection.startS);
    const canvas = document.createElement("canvas");
    canvas.width = frame.width;
    canvas.height = frame.height;
    const context = canvas.getContext("2d", { alpha: false });
    if (context === null) throw new Error("This browser cannot render the HEVC video.");
    context.drawImage(video, 0, 0, canvas.width, canvas.height);

    canvasStream = canvas.captureStream(frame.frameRate);
    if (!("captureStream" in video)) {
      throw new Error("This browser cannot convert native HEVC video.");
    }
    sourceStream = (video as CapturableVideo).captureStream();
    if (audioBitsPerSecond !== null) {
      const audioTrack = sourceStream.getAudioTracks()[0];
      if (audioTrack === undefined) {
        throw new Error("This browser cannot read the HEVC video's audio track.");
      }
      canvasStream.addTrack(audioTrack);
    }

    const recorder = new MediaRecorder(canvasStream, {
      mimeType,
      videoBitsPerSecond: Math.round(videoBitsPerSecond),
      ...(audioBitsPerSecond === null ? {} : { audioBitsPerSecond }),
    });
    const chunks: Blob[] = [];
    recorder.addEventListener("dataavailable", (event) => {
      if (event.data.size > 0) chunks.push(event.data);
    });

    let frameCallback: number | null = null;
    let animationFrame: number | null = null;
    let stopped = false;
    const durationS = selection.endS - selection.startS;
    const frameIntervalS = 1 / frame.frameRate;
    let nextFrameS = selection.startS;
    const stop = () => {
      if (stopped) return;
      stopped = true;
      video.pause();
      if (recorder.state !== "inactive") recorder.stop();
    };
    const draw = (mediaTime: number) => {
      onProgress(Math.min(Math.max((mediaTime - selection.startS) / durationS, 0), 1));
      if (mediaTime >= nextFrameS) {
        context.drawImage(video, 0, 0, canvas.width, canvas.height);
        nextFrameS = mediaTime + frameIntervalS;
      }
      if (mediaTime >= selection.endS) stop();
    };
    const frameVideo = video as FrameCallbackVideo;
    const scheduleFrame = () => {
      frameCallback = frameVideo.requestVideoFrameCallback((_now, metadata) => {
        draw(metadata.mediaTime);
        if (!stopped) scheduleFrame();
      });
    };
    const scheduleAnimationFrame = () => {
      animationFrame = window.requestAnimationFrame(() => {
        draw(video.currentTime);
        if (!stopped) scheduleAnimationFrame();
      });
    };

    const completion = new Promise<void>((resolve, reject) => {
      recorder.addEventListener("stop", () => resolve(), { once: true });
      recorder.addEventListener(
        "error",
        (event) => {
          const error = (event as Event & { error?: DOMException }).error;
          reject(error ?? new Error("The browser's video recorder failed."));
        },
        { once: true },
      );
    });
    let timedOut = false;
    const watchdog = window.setTimeout(
      () => {
        timedOut = true;
        stop();
      },
      Math.ceil((durationS + 10) * 1_000),
    );
    video.addEventListener("ended", stop, { once: true });
    recorder.start(1_000);
    if ("requestVideoFrameCallback" in video) scheduleFrame();
    else scheduleAnimationFrame();
    await video.play();
    await completion;
    window.clearTimeout(watchdog);
    if (frameCallback !== null) frameVideo.cancelVideoFrameCallback(frameCallback);
    if (animationFrame !== null) window.cancelAnimationFrame(animationFrame);
    if (timedOut) throw new Error("Timed out while converting this HEVC MOV file.");
    onProgress(1);

    return new Blob(chunks, { type: OUTPUT_CONTAINER_INFO[profile.format].mimeType });
  } finally {
    for (const track of canvasStream?.getTracks() ?? []) track.stop();
    for (const track of sourceStream?.getTracks() ?? []) track.stop();
    opened.dispose();
  }
}

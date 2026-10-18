import type { AudioPlan } from "./budget";
import type { PassReport } from "./convergence";
import type { VideoCodecChoice } from "./ladder";

export type OutputContainer = "mp4" | "webm";
export type AudioCodecChoice = "aac" | "opus" | "vorbis";

export interface OutputProfile {
  readonly format: OutputContainer;
  readonly videoCodec: VideoCodecChoice;
  readonly audioCodec: AudioCodecChoice | null;
}

// A session source: a local file from the dropzone, or a remote mp4 (an X
// post's CDN URL) that the engine reads with HTTP range requests.
export type MediaSource =
  | File
  | {
      readonly url: string;
      readonly name: string;
      // The resolve trace that produced this URL; rides along so failures
      // past the resolve hop (e.g. CDN fetch errors) still join to it.
      readonly traceId?: string;
    };

export interface VideoTrackSummary {
  readonly width: number;
  readonly height: number;
  readonly frameRate: number;
  readonly codec: string;
  readonly bitsPerSecond: number | undefined;
}

export type VideoDecodeMode = "webcodecs" | "media-element";

export interface ProbeSummary {
  readonly durationS: number;
  readonly container: string;
  readonly videoDecodeMode: VideoDecodeMode;
  readonly video: VideoTrackSummary;
  readonly hasAudio: boolean;
  readonly audioCodec: string | null;
  readonly outputProfiles: readonly OutputProfile[];
  // Presentation timestamps (seconds) of video keyframes, ascending.
  readonly keyframesS: readonly number[];
}

export const NATIVE_TRANSCODE_REQUIRED = "privatecut: native media-element transcode required";

export interface SelectionRange {
  readonly startS: number;
  readonly endS: number;
}

export type EncodeMode = "remux" | "transcode";

export interface FrameSize {
  readonly width: number;
  readonly height: number;
  readonly frameRate: number;
}

export type EncodePass = PassReport<FrameSize>;

export interface EncodeOutcome {
  readonly mode: EncodeMode;
  readonly outputFormat: OutputContainer;
  readonly bytes: number;
  readonly limitBytes: number;
  readonly utilization: number;
  readonly durationS: number;
  readonly width: number;
  readonly height: number;
  readonly frameRate: number;
  readonly codec: VideoCodecChoice | "source";
  readonly audio: AudioPlan | null;
  readonly passes: readonly EncodePass[];
  readonly wallMs: number;
}

// Where an encode was when it failed: choosing the plan, the stream-copy
// fast path, a WebCodecs transcode pass, or the native media-element path.
export type EncodeStage = "plan" | "remux" | "transcode" | "native";

// A failure as it crosses the worker boundary: Error's fields flattened
// (structured clone drops class identity) plus the encode's position.
export interface EncodeFailure {
  readonly name: string;
  readonly message: string;
  readonly stack: string;
  readonly stage: EncodeStage;
  readonly passes: readonly EncodePass[];
}

export type WorkerRequest =
  | { readonly kind: "probe"; readonly id: number; readonly source: MediaSource }
  | {
      readonly kind: "thumbnails";
      readonly id: number;
      readonly count: number;
      readonly height: number;
    }
  | {
      readonly kind: "encode";
      readonly id: number;
      readonly selection: SelectionRange;
      readonly limitBytes: number;
      readonly outputFormat: OutputContainer;
    };

export type WorkerResponse =
  | { readonly kind: "probed"; readonly id: number; readonly summary: ProbeSummary }
  | {
      readonly kind: "thumbnail";
      readonly id: number;
      readonly index: number;
      readonly timestampS: number;
      readonly bitmap: ImageBitmap;
    }
  | { readonly kind: "thumbnails-done"; readonly id: number }
  | {
      readonly kind: "progress";
      readonly id: number;
      readonly pass: number;
      readonly fraction: number;
      readonly frame: FrameSize;
    }
  | { readonly kind: "pass"; readonly id: number; readonly report: EncodePass }
  | {
      readonly kind: "encoded";
      readonly id: number;
      readonly blob: Blob;
      readonly outcome: EncodeOutcome;
    }
  | {
      readonly kind: "error";
      readonly id: number;
      readonly message: string;
      readonly failure?: EncodeFailure;
    }
  // An error the worker raised outside any request's control flow — a codec
  // callback throwing after its pipeline moved on, a stray rejection. Not
  // tied to a request id; reported, never fatal by itself.
  | {
      readonly kind: "fault";
      readonly name: string;
      readonly message: string;
      readonly stack: string;
    };

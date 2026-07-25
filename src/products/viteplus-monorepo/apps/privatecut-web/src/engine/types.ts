import type { PassRecord } from "./convergence";
import type { VideoCodecChoice } from "./ladder";

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

export interface ProbeSummary {
  readonly durationS: number;
  readonly video: VideoTrackSummary;
  readonly hasAudio: boolean;
  // Presentation timestamps (seconds) of video keyframes, ascending.
  readonly keyframesS: readonly number[];
}

export interface SelectionRange {
  readonly startS: number;
  readonly endS: number;
}

export type EncodeMode = "remux" | "transcode";

export interface EncodeOutcome {
  readonly mode: EncodeMode;
  readonly bytes: number;
  readonly utilization: number;
  readonly durationS: number;
  readonly width: number;
  readonly height: number;
  readonly frameRate: number;
  readonly codec: VideoCodecChoice | "source";
  readonly passes: readonly PassRecord[];
  readonly wallMs: number;
}

export type WorkerRequest =
  | { readonly kind: "probe"; readonly id: number; readonly source: MediaSource }
  | {
      readonly kind: "thumbnails";
      readonly id: number;
      readonly count: number;
      readonly height: number;
    }
  | { readonly kind: "encode"; readonly id: number; readonly selection: SelectionRange };

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
    }
  | {
      readonly kind: "encoded";
      readonly id: number;
      readonly blob: Blob;
      readonly outcome: EncodeOutcome;
    }
  | { readonly kind: "error"; readonly id: number; readonly message: string };

import type { PassRecord } from "./convergence";
import type { VideoCodecChoice } from "./ladder";

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
  | { readonly kind: "probe"; readonly id: number; readonly file: File }
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

// Main-thread handle on the encode worker. UI components talk to this class
// only. HEVC MOV files that the media element can play but WebCodecs cannot
// decode use the main thread's native video pipeline for frames.

import type { SizeLimitBytes } from "./limits";
import { assertNativePlayback, createNativeThumbnails, nativeTranscode } from "./native-media";
import type {
  EncodeOutcome,
  MediaSource,
  OutputContainer,
  ProbeSummary,
  SelectionRange,
  WorkerRequest,
  WorkerResponse,
} from "./types";
import { NATIVE_TRANSCODE_REQUIRED } from "./types";

export interface ThumbnailTile {
  readonly index: number;
  readonly timestampS: number;
  readonly bitmap: ImageBitmap;
}

export interface EncodeResult {
  readonly blob: Blob;
  readonly outcome: EncodeOutcome;
}

type DistributiveOmit<T, K extends PropertyKey> = T extends unknown ? Omit<T, K> : never;

interface PendingCall {
  resolve: (value: never) => void;
  reject: (reason: Error) => void;
  onThumbnail?: ((tile: ThumbnailTile) => void) | undefined;
  onProgress?: ((pass: number, fraction: number) => void) | undefined;
}

export class PrivateCutEngine {
  private readonly worker: Worker;
  private nextId = 1;
  private readonly pending = new Map<number, PendingCall>();
  private source: MediaSource | null = null;
  private summary: ProbeSummary | null = null;

  constructor() {
    this.worker = new Worker(new URL("./worker.ts", import.meta.url), { type: "module" });
    this.worker.onmessage = (event: MessageEvent<WorkerResponse>) => this.receive(event.data);
  }

  dispose(): void {
    this.worker.terminate();
    for (const call of this.pending.values()) {
      call.reject(new Error("Engine disposed."));
    }
    this.pending.clear();
  }

  async probe(source: MediaSource): Promise<ProbeSummary> {
    const summary = await this.call<ProbeSummary>({ kind: "probe", source });
    if (summary.videoDecodeMode === "media-element") {
      if (!(source instanceof File)) {
        throw new Error("Native HEVC decoding is only available for local files.");
      }
      await assertNativePlayback(source);
    }
    this.source = source;
    this.summary = summary;
    return summary;
  }

  thumbnails(
    count: number,
    height: number,
    onThumbnail: (tile: ThumbnailTile) => void,
  ): Promise<void> {
    if (this.summary?.videoDecodeMode === "media-element") {
      if (!(this.source instanceof File)) {
        return Promise.reject(new Error("Native HEVC decoding is only available for local files."));
      }
      return createNativeThumbnails(
        this.source,
        this.summary.durationS,
        count,
        height,
        onThumbnail,
      );
    }
    return this.call({ kind: "thumbnails", count, height }, { onThumbnail });
  }

  async encode(
    selection: SelectionRange,
    limitBytes: SizeLimitBytes,
    outputFormat: OutputContainer,
    onProgress: (pass: number, fraction: number) => void,
  ): Promise<EncodeResult> {
    try {
      return await this.call(
        { kind: "encode", selection, limitBytes, outputFormat },
        { onProgress },
      );
    } catch (error) {
      if (
        !(error instanceof Error) ||
        error.message !== NATIVE_TRANSCODE_REQUIRED ||
        !(this.source instanceof File) ||
        this.summary?.videoDecodeMode !== "media-element"
      ) {
        throw error;
      }
      return nativeTranscode({
        file: this.source,
        summary: this.summary,
        selection,
        limitBytes,
        outputFormat,
        onProgress,
      });
    }
  }

  private call<T>(
    request: DistributiveOmit<WorkerRequest, "id">,
    handlers: Pick<PendingCall, "onThumbnail" | "onProgress"> = {},
  ): Promise<T> {
    const id = this.nextId;
    this.nextId += 1;
    return new Promise<T>((resolve, reject) => {
      this.pending.set(id, {
        resolve: resolve as (value: never) => void,
        reject,
        onThumbnail: handlers.onThumbnail,
        onProgress: handlers.onProgress,
      });
      this.worker.postMessage({ ...request, id } as WorkerRequest);
    });
  }

  private receive(response: WorkerResponse): void {
    const call = this.pending.get(response.id);
    if (call === undefined) return;
    switch (response.kind) {
      case "probed":
        this.pending.delete(response.id);
        call.resolve(response.summary as never);
        break;
      case "thumbnail":
        call.onThumbnail?.({
          index: response.index,
          timestampS: response.timestampS,
          bitmap: response.bitmap,
        });
        break;
      case "thumbnails-done":
        this.pending.delete(response.id);
        call.resolve(undefined as never);
        break;
      case "progress":
        call.onProgress?.(response.pass, response.fraction);
        break;
      case "encoded":
        this.pending.delete(response.id);
        call.resolve({ blob: response.blob, outcome: response.outcome } as never);
        break;
      case "error":
        this.pending.delete(response.id);
        call.reject(new Error(response.message));
        break;
    }
  }
}

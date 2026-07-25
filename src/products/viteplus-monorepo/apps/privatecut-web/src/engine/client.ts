// Main-thread handle on the encode worker. UI components talk to this class
// only; mediabunny itself never loads on the main thread.

import type {
  EncodeOutcome,
  MediaSource,
  ProbeSummary,
  SelectionRange,
  WorkerRequest,
  WorkerResponse,
} from "./types";

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

  probe(source: MediaSource): Promise<ProbeSummary> {
    return this.call({ kind: "probe", source });
  }

  thumbnails(
    count: number,
    height: number,
    onThumbnail: (tile: ThumbnailTile) => void,
  ): Promise<void> {
    return this.call({ kind: "thumbnails", count, height }, { onThumbnail });
  }

  encode(
    selection: SelectionRange,
    onProgress: (pass: number, fraction: number) => void,
  ): Promise<EncodeResult> {
    return this.call({ kind: "encode", selection }, { onProgress });
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

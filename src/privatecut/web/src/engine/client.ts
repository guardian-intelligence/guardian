// Main-thread handle on the encode worker. UI components talk to this class
// only. HEVC MOV files that the media element can play but WebCodecs cannot
// decode use the main thread's native video pipeline for frames.

import { ConvergenceError } from "./convergence";
import type { SizeLimitBytes } from "./limits";
import { assertNativePlayback, createNativeThumbnails, nativeTranscode } from "./native-media";
import type {
  EncodeOutcome,
  EncodePass,
  EncodeStage,
  FrameSize,
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

export interface EncodeHandlers {
  readonly onProgress: (pass: number, fraction: number, frame: FrameSize) => void;
  readonly onPass: (report: EncodePass) => void;
}

// How an encode failed: the pipeline threw (`failed`), stopped making
// progress without ever throwing (`stalled`), or the worker itself died
// (`worker-lost`).
export type EngineFailureKind = "failed" | "stalled" | "worker-lost";

export class EngineError extends Error {
  override readonly name = "EngineError";
  constructor(
    message: string,
    readonly kind: EngineFailureKind,
    // The originating error's class name (EncodingError, ConvergenceError…).
    readonly causeName: string,
    readonly stage: EncodeStage | null,
    readonly passes: readonly EncodePass[],
    causeStack = "",
  ) {
    super(message);
    if (causeStack !== "") this.stack = causeStack;
  }
}

// An error raised inside the worker outside any request (see worker.ts).
export interface WorkerFault {
  readonly name: string;
  readonly message: string;
  readonly stack: string;
}

export interface EngineOptions {
  readonly onFault?: (fault: WorkerFault) => void;
}

// A wedged codec neither errors nor finishes: when the GPU process dies
// under a VideoDecoder, the conversion awaiting its frames can hang forever.
// Progress is the only liveness signal that means anything (a timer inside
// the worker keeps ticking while its codecs are dead), so an encode whose
// progress stops advancing for this long is declared stalled.
const STALL_MS = 30_000;

type DistributiveOmit<T, K extends PropertyKey> = T extends unknown ? Omit<T, K> : never;

interface PendingCall {
  resolve: (value: never) => void;
  reject: (reason: Error) => void;
  onThumbnail?: ((tile: ThumbnailTile) => void) | undefined;
  onProgress?: EncodeHandlers["onProgress"] | undefined;
  onPass?: EncodeHandlers["onPass"] | undefined;
}

export class PrivateCutEngine {
  private worker: Worker;
  private nextId = 1;
  private readonly pending = new Map<number, PendingCall>();
  private source: MediaSource | null = null;
  private summary: ProbeSummary | null = null;
  // Settles once a recycled worker has reopened the source; every call
  // waits on it so nothing reaches a worker with no open file.
  private ready: Promise<void> = Promise.resolve();
  private readonly onFault: ((fault: WorkerFault) => void) | undefined;

  constructor(options: EngineOptions = {}) {
    this.onFault = options.onFault;
    this.worker = this.spawn();
  }

  dispose(): void {
    this.worker.terminate();
    this.rejectAll(new Error("Engine disposed."));
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
    handlers: EncodeHandlers,
  ): Promise<EncodeResult> {
    try {
      return await this.watched(handlers, (watchedHandlers) =>
        this.call({ kind: "encode", selection, limitBytes, outputFormat }, watchedHandlers),
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
      const { source: file, summary } = this;
      return this.watched(handlers, (watchedHandlers) =>
        nativeTranscode({ file, summary, selection, limitBytes, outputFormat, ...watchedHandlers })
          // The native path runs on this thread; give its failures the same
          // shape the worker's carry.
          .catch((nativeError: unknown) => {
            throw toEngineError(nativeError, "native");
          }),
      );
    }
  }

  // Runs one encode under the stall watchdog. A stall tears the worker down
  // with whatever it holds and brings up a fresh one on the same source, so
  // the user's next attempt starts clean.
  private watched<T>(
    handlers: EncodeHandlers,
    run: (handlers: EncodeHandlers) => Promise<T>,
  ): Promise<T> {
    let lastAdvanceAt = performance.now();
    let lastKey = "";
    let passes: EncodePass[] = [];
    let lastPass = 0;
    const watchedHandlers: EncodeHandlers = {
      onProgress: (pass, fraction, frame) => {
        const key = `${pass}:${fraction.toFixed(3)}`;
        if (key !== lastKey) {
          lastKey = key;
          lastAdvanceAt = performance.now();
        }
        lastPass = pass;
        handlers.onProgress(pass, fraction, frame);
      },
      onPass: (report) => {
        lastAdvanceAt = performance.now();
        passes = [...passes, report];
        handlers.onPass(report);
      },
    };
    return new Promise<T>((resolve, reject) => {
      const watchdog = window.setInterval(() => {
        if (performance.now() - lastAdvanceAt < STALL_MS) return;
        window.clearInterval(watchdog);
        const stalled = new EngineError(
          `The encoder stopped responding during pass ${Math.max(lastPass, 1)}.`,
          "stalled",
          "EncoderStall",
          lastPass === 0 && passes.length === 0 ? null : "transcode",
          passes,
        );
        this.recycle(stalled);
        reject(stalled);
      }, 1_000);
      run(watchedHandlers).then(
        (value) => {
          window.clearInterval(watchdog);
          resolve(value);
        },
        (error: unknown) => {
          window.clearInterval(watchdog);
          reject(error instanceof Error ? error : new Error(String(error)));
        },
      );
    });
  }

  private spawn(): Worker {
    const worker = new Worker(new URL("./worker.ts", import.meta.url), { type: "module" });
    worker.onmessage = (event: MessageEvent<WorkerResponse>) => this.receive(event.data);
    // The worker reports its own runtime errors as faults (and suppresses
    // them), so reaching here means the worker failed to load or died.
    worker.onerror = (event: ErrorEvent) => {
      event.preventDefault();
      this.rejectAll(
        new EngineError(
          event.message || "The encoder worker stopped unexpectedly.",
          "worker-lost",
          "WorkerError",
          null,
          [],
        ),
      );
    };
    return worker;
  }

  private recycle(reason: Error): void {
    this.worker.terminate();
    this.rejectAll(reason);
    this.worker = this.spawn();
    const { source } = this;
    if (source === null) return;
    this.ready = this.send<ProbeSummary>({ kind: "probe", source }).then(
      () => undefined,
      () => undefined,
    );
  }

  private rejectAll(reason: Error): void {
    for (const call of this.pending.values()) {
      call.reject(reason);
    }
    this.pending.clear();
  }

  private async call<T>(
    request: DistributiveOmit<WorkerRequest, "id">,
    handlers: Pick<PendingCall, "onThumbnail" | "onProgress" | "onPass"> = {},
  ): Promise<T> {
    await this.ready;
    return this.send<T>(request, handlers);
  }

  private send<T>(
    request: DistributiveOmit<WorkerRequest, "id">,
    handlers: Pick<PendingCall, "onThumbnail" | "onProgress" | "onPass"> = {},
  ): Promise<T> {
    const id = this.nextId;
    this.nextId += 1;
    return new Promise<T>((resolve, reject) => {
      this.pending.set(id, {
        resolve: resolve as (value: never) => void,
        reject,
        onThumbnail: handlers.onThumbnail,
        onProgress: handlers.onProgress,
        onPass: handlers.onPass,
      });
      this.worker.postMessage({ ...request, id } as WorkerRequest);
    });
  }

  private receive(response: WorkerResponse): void {
    if (response.kind === "fault") {
      this.onFault?.({ name: response.name, message: response.message, stack: response.stack });
      return;
    }
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
        call.onProgress?.(response.pass, response.fraction, response.frame);
        break;
      case "pass":
        call.onPass?.(response.report);
        break;
      case "encoded":
        this.pending.delete(response.id);
        call.resolve({ blob: response.blob, outcome: response.outcome } as never);
        break;
      case "error": {
        this.pending.delete(response.id);
        const { failure } = response;
        call.reject(
          failure === undefined
            ? new Error(response.message)
            : new EngineError(
                failure.message,
                "failed",
                failure.name,
                failure.stage,
                failure.passes,
                failure.stack,
              ),
        );
        break;
      }
    }
  }
}

function toEngineError(error: unknown, stage: EncodeStage): EngineError {
  if (error instanceof EngineError) return error;
  const cause = error instanceof Error ? error : new Error(String(error));
  return new EngineError(
    cause.message,
    "failed",
    cause.name,
    stage,
    cause instanceof ConvergenceError ? (cause.passes as readonly EncodePass[]) : [],
    cause.stack ?? "",
  );
}

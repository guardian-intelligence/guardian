/// <reference lib="webworker" />

// All media work lives here, off the main thread. The protocol is
// request/response over ids (types.ts); the only large values that cross the
// boundary are transferable (ImageBitmaps out, one Blob per encode).

import { CanvasSink } from "mediabunny";
import { planBudget } from "./budget";
import { calibratedMargin, recordCalibration } from "./calibration";
import { converge, firstPassBitrate } from "./convergence";
import { OUTPUT_CONTAINER_INFO } from "./formats";
import { planFrame, stepDownFrame, type FramePlan, type FramePlanInput } from "./ladder";
import { isSizeLimit, MAX_SELECTION_SECONDS } from "./limits";
import { resolveOutputProfile } from "./output";
import type { OpenedInput } from "./probe";
import { openInput } from "./probe";
import { executeRemux, planRemux } from "./remux";
import { transcodePass } from "./transcode";
import {
  NATIVE_TRANSCODE_REQUIRED,
  type EncodeFailure,
  type EncodeOutcome,
  type EncodePass,
  type EncodeStage,
  type FrameSize,
  type SelectionRange,
  type WorkerRequest,
  type WorkerResponse,
} from "./types";

let opened: OpenedInput | null = null;

function post(response: WorkerResponse, transfer: Transferable[] = []): void {
  (self as unknown as DedicatedWorkerGlobalScope).postMessage(response, transfer);
}

self.onmessage = (event: MessageEvent<WorkerRequest>) => {
  void dispatch(event.data);
};

// Codec callbacks can throw after the pipeline that owned them has moved on
// (a decoder erroring as the GPU process dies, mid-teardown). Those never
// reach a request's catch; surface them instead of leaving them to the
// console, where production cannot see them.
function postFault(reason: unknown): void {
  const error = reason instanceof Error ? reason : new Error(String(reason));
  post({ kind: "fault", name: error.name, message: error.message, stack: error.stack ?? "" });
}
self.addEventListener("error", (event: ErrorEvent) => {
  event.preventDefault();
  postFault(event.error ?? event.message);
});
self.addEventListener("unhandledrejection", (event: PromiseRejectionEvent) => {
  event.preventDefault();
  postFault(event.reason);
});

// Where a running encode is, kept current so a failure can say where it
// happened and what was tried.
interface EncodeTrail {
  stage: EncodeStage;
  readonly passes: EncodePass[];
}

// An encode failure annotated from its trail.
class StagedFailure extends Error {
  readonly failure: EncodeFailure;
  constructor(error: unknown, trail: EncodeTrail) {
    const cause = error instanceof Error ? error : new Error(String(error));
    super(cause.message);
    this.failure = {
      name: cause.name,
      message: cause.message,
      stack: cause.stack ?? "",
      stage: trail.stage,
      passes: trail.passes,
    };
  }
}

async function dispatch(request: WorkerRequest): Promise<void> {
  try {
    switch (request.kind) {
      case "probe": {
        opened = await openInput(request.source);
        post({ kind: "probed", id: request.id, summary: opened.summary });
        break;
      }
      case "thumbnails": {
        if (opened === null) throw new Error("No open file.");
        if (opened.summary.videoDecodeMode === "media-element") {
          throw new Error("Native thumbnails must be created on the main thread.");
        }
        await streamThumbnails(request.id, request.count, request.height);
        break;
      }
      case "encode": {
        if (opened === null) throw new Error("No open file.");
        await encode(request.id, request.selection, request.limitBytes, request.outputFormat);
        break;
      }
    }
  } catch (error) {
    post({
      kind: "error",
      id: request.id,
      message: error instanceof Error ? error.message : String(error),
      ...(error instanceof StagedFailure && { failure: error.failure }),
    });
  }
}

async function streamThumbnails(id: number, count: number, height: number): Promise<void> {
  if (opened === null) return;
  const sink = new CanvasSink(opened.videoTrack, { height, poolSize: 2 });
  const duration = opened.summary.durationS;
  const timestamps = Array.from({ length: count }, (_, i) => (duration * (i + 0.5)) / count);
  let index = 0;
  for await (const wrapped of sink.canvasesAtTimestamps(timestamps)) {
    if (wrapped !== null) {
      const bitmap = await createImageBitmap(wrapped.canvas);
      post({ kind: "thumbnail", id, index, timestampS: wrapped.timestamp, bitmap }, [bitmap]);
    }
    index += 1;
  }
  post({ kind: "thumbnails-done", id });
}

function frameSize(frame: FramePlan): FrameSize {
  return { width: frame.width, height: frame.height, frameRate: frame.frameRate };
}

async function encode(
  id: number,
  selection: SelectionRange,
  limitBytes: number,
  outputFormat: EncodeOutcome["outputFormat"],
): Promise<void> {
  if (opened === null) return;
  const trail: EncodeTrail = { stage: "plan", passes: [] };
  try {
    await runEncode(id, opened, selection, limitBytes, outputFormat, trail);
  } catch (error) {
    if (error instanceof Error && error.message === NATIVE_TRANSCODE_REQUIRED) throw error;
    throw new StagedFailure(error, trail);
  }
}

async function runEncode(
  id: number,
  openedInput: OpenedInput,
  selection: SelectionRange,
  limitBytes: number,
  outputFormat: EncodeOutcome["outputFormat"],
  trail: EncodeTrail,
): Promise<void> {
  if (!isSizeLimit(limitBytes)) throw new Error("Unsupported size cap.");
  const profile = await resolveOutputProfile(outputFormat, openedInput.audioTrack !== null);
  if (profile === null) {
    throw new Error(`This browser cannot encode ${OUTPUT_CONTAINER_INFO[outputFormat].label}.`);
  }
  const startedAt = performance.now();
  const clamped: SelectionRange = {
    startS: Math.max(selection.startS, 0),
    endS: Math.min(selection.endS, openedInput.summary.durationS),
  };
  const durationS = clamped.endS - clamped.startS;
  if (durationS <= 0) throw new Error("Empty selection.");
  if (durationS > MAX_SELECTION_SECONDS + 0.001) {
    throw new Error(`Selections are capped at ${MAX_SELECTION_SECONDS} seconds.`);
  }
  const { summary } = openedInput;

  // Fast path: lossless stream copy when the selection allows it.
  trail.stage = "remux";
  const remuxPlan = await planRemux(openedInput, clamped, limitBytes, outputFormat);
  if (remuxPlan !== null) {
    const remuxed = await executeRemux(openedInput, remuxPlan, outputFormat);
    if (remuxed !== null && remuxed.bytes <= limitBytes) {
      finish(id, remuxed.buffer, {
        mode: "remux",
        outputFormat,
        bytes: remuxed.bytes,
        limitBytes,
        utilization: remuxed.bytes / limitBytes,
        durationS,
        width: summary.video.width,
        height: summary.video.height,
        frameRate: summary.video.frameRate,
        codec: "source",
        audio: null,
        passes: [],
        wallMs: performance.now() - startedAt,
      });
      return;
    }
  }
  if (summary.videoDecodeMode === "media-element") {
    throw new Error(NATIVE_TRANSCODE_REQUIRED);
  }

  trail.stage = "transcode";
  const budget = planBudget({
    durationS,
    frameRate: summary.video.frameRate,
    sourceHasAudio: openedInput.audioTrack !== null,
    audioCodec: profile.audioCodec,
    sourceVideoBitsPerSecond: summary.video.bitsPerSecond,
    limitBytes,
  });
  const frameInput: FramePlanInput = {
    sourceWidth: summary.video.width,
    sourceHeight: summary.video.height,
    sourceFrameRate: summary.video.frameRate,
    videoBitsPerSecond: budget.videoBitsPerSecond,
    codec: profile.videoCodec,
  };
  const frame = planFrame(frameInput);
  const margin = calibratedMargin(profile.videoCodec, frame.height);

  const result = await converge<ArrayBuffer, FramePlan>({
    limitBytes,
    reservedBytes: budget.containerBytes + budget.audioBytes,
    initialVideoBitsPerSecond: budget.videoBitsPerSecond,
    maxVideoBitsPerSecond: summary.video.bitsPerSecond,
    safetyMargin: margin,
    frame,
    stepDown: (current) => stepDownFrame(current, frameInput),
    encode: async (videoBitsPerSecond, pass, passFrame) => {
      const outcome = await transcodePass({
        opened: openedInput,
        selection: clamped,
        frame: passFrame,
        budget,
        profile,
        videoBitsPerSecond,
        onProgress: (fraction) =>
          post({ kind: "progress", id, pass, fraction, frame: frameSize(passFrame) }),
      });
      return { bytes: outcome.bytes, artifact: outcome.buffer };
    },
    onPass: (report) => {
      const encodePass: EncodePass = { ...report, frame: frameSize(report.frame) };
      trail.passes.push(encodePass);
      post({ kind: "pass", id, report: encodePass });
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

  finish(id, result.artifact, {
    mode: "transcode",
    outputFormat,
    bytes: result.bytes,
    limitBytes,
    utilization: result.utilization,
    durationS,
    width: result.frame.width,
    height: result.frame.height,
    frameRate: result.frame.frameRate,
    codec: profile.videoCodec,
    audio: budget.audio,
    passes: trail.passes,
    wallMs: performance.now() - startedAt,
  });
}

function finish(id: number, buffer: ArrayBuffer, outcome: EncodeOutcome): void {
  // The gate, restated at the last exit: nothing over the cap leaves the
  // worker, whatever path produced it.
  if (outcome.bytes > outcome.limitBytes) {
    post({ kind: "error", id, message: "Internal error: output exceeded the size cap." });
    return;
  }
  const blob = new Blob([buffer], {
    type: OUTPUT_CONTAINER_INFO[outcome.outputFormat].mimeType,
  });
  post({ kind: "encoded", id, blob, outcome });
}

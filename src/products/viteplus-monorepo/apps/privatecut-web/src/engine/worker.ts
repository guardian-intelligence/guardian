/// <reference lib="webworker" />

// All media work lives here, off the main thread. The protocol is
// request/response over ids (types.ts); the only large values that cross the
// boundary are transferable (ImageBitmaps out, one Blob per encode).

import { CanvasSink } from "mediabunny";
import { planBudget } from "./budget";
import { calibratedMargin, recordCalibration } from "./calibration";
import { converge, firstPassBitrate } from "./convergence";
import { OUTPUT_CONTAINER_INFO } from "./formats";
import { planFrame } from "./ladder";
import { isSizeLimit, MAX_SELECTION_SECONDS } from "./limits";
import { resolveOutputProfile } from "./output";
import type { OpenedInput } from "./probe";
import { openInput } from "./probe";
import { executeRemux, planRemux } from "./remux";
import { transcodePass } from "./transcode";
import type { EncodeOutcome, SelectionRange, WorkerRequest, WorkerResponse } from "./types";

let opened: OpenedInput | null = null;

function post(response: WorkerResponse, transfer: Transferable[] = []): void {
  (self as unknown as DedicatedWorkerGlobalScope).postMessage(response, transfer);
}

self.onmessage = (event: MessageEvent<WorkerRequest>) => {
  void dispatch(event.data);
};

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

async function encode(
  id: number,
  selection: SelectionRange,
  limitBytes: number,
  outputFormat: EncodeOutcome["outputFormat"],
): Promise<void> {
  if (opened === null) return;
  if (!isSizeLimit(limitBytes)) throw new Error("Unsupported size cap.");
  const profile = await resolveOutputProfile(outputFormat, opened.audioTrack !== null);
  if (profile === null) {
    throw new Error(`This browser cannot encode ${OUTPUT_CONTAINER_INFO[outputFormat].label}.`);
  }
  const startedAt = performance.now();
  const clamped: SelectionRange = {
    startS: Math.max(selection.startS, 0),
    endS: Math.min(selection.endS, opened.summary.durationS),
  };
  const durationS = clamped.endS - clamped.startS;
  if (durationS <= 0) throw new Error("Empty selection.");
  if (durationS > MAX_SELECTION_SECONDS + 0.001) {
    throw new Error(`Selections are capped at ${MAX_SELECTION_SECONDS} seconds.`);
  }

  // Fast path: lossless stream copy when the selection allows it.
  const remuxPlan = await planRemux(opened, clamped, limitBytes, outputFormat);
  if (remuxPlan !== null) {
    const remuxed = await executeRemux(opened, remuxPlan, outputFormat);
    if (remuxed !== null && remuxed.bytes <= limitBytes) {
      const { summary } = opened;
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
        passes: [],
        wallMs: performance.now() - startedAt,
      });
      return;
    }
  }

  const { summary } = opened;
  const budget = planBudget({
    durationS,
    frameRate: summary.video.frameRate,
    sourceHasAudio: opened.audioTrack !== null,
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
  const openedInput = opened;

  const result = await converge<ArrayBuffer>({
    limitBytes,
    reservedBytes: budget.containerBytes + budget.audioBytes,
    initialVideoBitsPerSecond: budget.videoBitsPerSecond,
    maxVideoBitsPerSecond: summary.video.bitsPerSecond,
    safetyMargin: margin,
    encode: async (videoBitsPerSecond, pass) => {
      const outcome = await transcodePass({
        opened: openedInput,
        selection: clamped,
        frame,
        budget,
        profile,
        videoBitsPerSecond,
        onProgress: (fraction) => post({ kind: "progress", id, pass, fraction }),
      });
      return { bytes: outcome.bytes, artifact: outcome.buffer };
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
    width: frame.width,
    height: frame.height,
    frameRate: frame.frameRate,
    codec: profile.videoCodec,
    passes: result.passes,
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

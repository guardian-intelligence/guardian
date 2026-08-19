// The error event's shape contract: what reportError queues for the beacon
// is the evidence dashboards group by. Platform error types that carry
// their meaning in typed fields (WebTransportError's source and
// streamErrorCode) surface those in the message, and no error ever queues
// with an empty message — empty messages collapse unrelated failures into
// one signature.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { TelemetryEvent } from "../src/browser";
import { reportError } from "../src/errors";

class WebTransportErrorLike extends Error {
  override name = "WebTransportError";
  source: string;
  streamErrorCode: number | null;
  constructor(message: string, source: string, streamErrorCode: number | null) {
    super(message);
    this.source = source;
    this.streamErrorCode = streamErrorCode;
  }
}

let queue: TelemetryEvent[];

beforeEach(() => {
  queue = [];
  vi.stubGlobal("window", {
    location: { pathname: "/park" },
    __guardianEvents: queue,
  });
});
afterEach(() => {
  vi.unstubAllGlobals();
});

function lastEvent(): TelemetryEvent {
  const e = queue[queue.length - 1];
  if (!e) throw new Error("no event queued");
  return e;
}

describe("reportError", () => {
  it("queues an error event carrying message, type, and caller attrs", () => {
    const id = reportError(new Error("dial exploded"), { "error.op": "transport.dial" });

    const event = lastEvent();
    expect(event.name).toBe("error");
    expect(event.attrs["error.kind"]).toBe("reported");
    expect(event.attrs["error.type"]).toBe("Error");
    expect(event.attrs["error.message"]).toBe("dial exploded");
    expect(event.attrs["error.op"]).toBe("transport.dial");
    expect(event.attrs["route.path"]).toBe("/park");
    expect(event.attrs["trace.id"]).toBe(id);
    expect(id).toMatch(/^[0-9a-f]{32}$/);
  });

  it("folds WebTransportError fields into an otherwise empty message", () => {
    reportError(new WebTransportErrorLike("", "session", 0));

    const event = lastEvent();
    expect(event.attrs["error.type"]).toBe("WebTransportError");
    expect(event.attrs["error.message"]).toBe("[source=session streamErrorCode=0]");
  });

  it("appends the typed fields after a real message", () => {
    reportError(new WebTransportErrorLike("stream reset", "stream", 7));

    expect(lastEvent().attrs["error.message"]).toBe(
      "stream reset [source=stream streamErrorCode=7]",
    );
  });

  it("skips a null streamErrorCode and keeps the source", () => {
    reportError(new WebTransportErrorLike("", "session", null));

    expect(lastEvent().attrs["error.message"]).toBe("[source=session]");
  });

  it("falls back to the error's name when nothing else names it", () => {
    const bare = new Error("");
    bare.name = "AbortError";
    reportError(bare);

    const event = lastEvent();
    expect(event.attrs["error.type"]).toBe("AbortError");
    expect(event.attrs["error.message"]).toBe("AbortError");
  });

  it("reports non-Error values by type and string form", () => {
    reportError("wire fell out");

    const event = lastEvent();
    expect(event.attrs["error.type"]).toBe("string");
    expect(event.attrs["error.message"]).toBe("wire fell out");
  });
});

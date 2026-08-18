// The uplink contract, proven against a scripted WebTransport fake: writes
// are awaited and ordered, a failed or hung write tears down through
// onClosed(reason) exactly once, and the datagram lane enforces its
// budget silently.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CloseReason, ConnectionSink } from "@guardian/chunkies";
import { createTransport } from "../src/index.ts";

type WriteMode = "ok" | "reject" | "hang";

class FakeWebTransport {
  static last: FakeWebTransport | undefined;
  static failDial = false;

  ready: Promise<void>;
  closed: Promise<void>;
  #resolveClosed!: () => void;
  closeCalls = 0;

  written: Uint8Array[] = [];
  datagramsSent: Uint8Array[] = [];
  writeMode: WriteMode = "ok";
  maxDatagramSize = 1350;

  datagrams: {
    readable: ReadableStream<Uint8Array>;
    writable: WritableStream<Uint8Array>;
    maxDatagramSize: number;
  };

  constructor(_url: string, _opts?: unknown) {
    FakeWebTransport.last = this;
    this.ready = FakeWebTransport.failDial ? new Promise<void>(() => {}) : Promise.resolve();
    this.closed = new Promise<void>((resolve) => {
      this.#resolveClosed = resolve;
    });
    const self = this;
    this.datagrams = {
      readable: new ReadableStream<Uint8Array>({ start() {} }),
      writable: new WritableStream<Uint8Array>({
        write(chunk) {
          self.datagramsSent.push(chunk.slice());
        },
      }),
      maxDatagramSize: this.maxDatagramSize,
    };
  }

  async createBidirectionalStream() {
    const self = this;
    return {
      readable: new ReadableStream<Uint8Array>({ start() {} }),
      writable: new WritableStream<Uint8Array>({
        write(chunk) {
          if (self.writeMode === "reject") return Promise.reject(new Error("path broke"));
          if (self.writeMode === "hang") return new Promise<void>(() => {});
          self.written.push(chunk.slice());
          return Promise.resolve();
        },
      }),
    };
  }

  close() {
    this.closeCalls++;
    this.#resolveClosed();
  }
}

function sinkRecorder() {
  const reasons: (CloseReason | undefined)[] = [];
  const sink: ConnectionSink = {
    onStreamBytes: () => {},
    onDatagram: () => {},
    onClosed: (reason) => reasons.push(reason),
  };
  return { sink, reasons };
}

const mint = () =>
  Promise.resolve({ ticket: "tkt", endpoint: "127.0.0.1:4433", certHashB64: undefined });

async function settled(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

beforeEach(() => {
  vi.stubGlobal("WebTransport", FakeWebTransport);
  FakeWebTransport.failDial = false;
  FakeWebTransport.last = undefined;
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("uplink", () => {
  it("drains queued frames in order through awaited writes", async () => {
    const { sink } = sinkRecorder();
    const { connection } = await createTransport({ mint }).connect(sink);
    const fake = FakeWebTransport.last!;

    connection.sendFrame(Uint8Array.of(1));
    connection.sendFrame(Uint8Array.of(2, 2));
    connection.sendFrame(Uint8Array.of(3, 3, 3));
    await settled();
    await settled();

    expect(fake.written.map((f) => f.length)).toEqual([1, 2, 3]);
  });

  it("tears down once with write-error when a write rejects", async () => {
    const { sink, reasons } = sinkRecorder();
    const { connection } = await createTransport({ mint }).connect(sink);
    FakeWebTransport.last!.writeMode = "reject";

    connection.sendFrame(Uint8Array.of(9));
    await settled();
    await settled();

    expect(reasons).toEqual(["write-error"]);
    // The transport.closed settling afterwards must not double-report.
    await settled();
    expect(reasons).toHaveLength(1);
    expect(FakeWebTransport.last!.closeCalls).toBeGreaterThan(0);
  });

  it("tears down with stall when a write hangs past the deadline", async () => {
    vi.useFakeTimers();
    const { sink, reasons } = sinkRecorder();
    const { connection } = await createTransport({ mint }).connect(sink);
    FakeWebTransport.last!.writeMode = "hang";

    connection.sendFrame(Uint8Array.of(9));
    await vi.advanceTimersByTimeAsync(4_999);
    expect(reasons).toEqual([]);
    await vi.advanceTimersByTimeAsync(2);
    expect(reasons).toEqual(["stall"]);
  });

  it("treats queue overflow as a dead path", async () => {
    const { sink, reasons } = sinkRecorder();
    const { connection } = await createTransport({ mint }).connect(sink);
    FakeWebTransport.last!.writeMode = "hang";

    connection.sendFrame(new Uint8Array(20 * 1024));
    connection.sendFrame(new Uint8Array(20 * 1024));
    await settled();

    expect(reasons).toEqual(["stall"]);
  });
});

describe("datagram lane", () => {
  it("reports the negotiated budget, flooring at 1200 when unknown", async () => {
    const { sink } = sinkRecorder();
    const { connection } = await createTransport({ mint }).connect(sink);
    expect(connection.datagramBudget()).toBe(1350);

    FakeWebTransport.last!.datagrams.maxDatagramSize = 0;
    expect(connection.datagramBudget()).toBe(1200);
  });

  it("drops oversize datagrams silently and sends the rest", async () => {
    const { sink, reasons } = sinkRecorder();
    const { connection } = await createTransport({ mint }).connect(sink);
    const fake = FakeWebTransport.last!;

    connection.sendDatagram(new Uint8Array(1351));
    connection.sendDatagram(Uint8Array.of(1, 2, 3));
    await settled();

    expect(fake.datagramsSent.map((d) => d.length)).toEqual([3]);
    expect(reasons).toEqual([]);
  });
});

describe("lifecycle", () => {
  it("onClosed fires exactly once across close() and transport.closed", async () => {
    const { sink, reasons } = sinkRecorder();
    const { connection } = await createTransport({ mint }).connect(sink);

    connection.close();
    connection.close();
    await settled();
    await settled();

    expect(reasons).toEqual(["transport"]);
  });

  it("a dial that never settles rejects at the deadline", async () => {
    vi.useFakeTimers();
    FakeWebTransport.failDial = true;
    const { sink } = sinkRecorder();
    const pending = createTransport({ mint }).connect(sink);
    const outcome = pending.then(
      () => "connected",
      () => "rejected",
    );
    await vi.advanceTimersByTimeAsync(10_001);
    expect(await outcome).toBe("rejected");
    expect(FakeWebTransport.last!.closeCalls).toBeGreaterThan(0);
  });
});

// The uplink contract, proven against a scripted WebTransport fake: writes
// are awaited and ordered, a failed or hung write tears down through
// onClosed(reason) exactly once, and the datagram lane enforces its
// budget silently. Errors are data: every failure the transport absorbs
// reaches onError carrying the phase that produced it, and our own
// teardown reaches it never.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CloseReason, ConnectionSink } from "@guardian/chunkies";
import { createTransport } from "../src/index.ts";

type WriteMode = "ok" | "reject" | "hang";

class FakeWebTransport {
  static last: FakeWebTransport | undefined;
  static failDial = false;
  static failOpen = false;

  ready: Promise<void>;
  closed: Promise<void>;
  #resolveClosed!: () => void;
  #rejectClosed!: (e: unknown) => void;
  /** Set when failDial: settles the pending `ready` late, like a network
   * stack reporting the real failure after the dial deadline passed. */
  rejectReady: (e: unknown) => void = () => {};
  closeCalls = 0;

  written: Uint8Array[] = [];
  datagramsSent: Uint8Array[] = [];
  writeMode: WriteMode = "ok";
  datagramWriteMode: WriteMode = "ok";
  maxDatagramSize = 1350;

  datagrams: {
    readable: ReadableStream<Uint8Array>;
    writable: WritableStream<Uint8Array>;
    maxDatagramSize: number;
  };

  constructor(_url: string, _opts?: unknown) {
    FakeWebTransport.last = this;
    this.ready = FakeWebTransport.failDial
      ? new Promise<void>((_, reject) => {
          this.rejectReady = reject;
        })
      : Promise.resolve();
    this.closed = new Promise<void>((resolve, reject) => {
      this.#resolveClosed = resolve;
      this.#rejectClosed = reject;
    });
    const self = this;
    this.datagrams = {
      readable: new ReadableStream<Uint8Array>({ start() {} }),
      writable: new WritableStream<Uint8Array>({
        write(chunk) {
          if (self.datagramWriteMode === "reject")
            return Promise.reject(new Error("datagram lane broke"));
          self.datagramsSent.push(chunk.slice());
          return Promise.resolve();
        },
      }),
      maxDatagramSize: this.maxDatagramSize,
    };
  }

  async createBidirectionalStream() {
    if (FakeWebTransport.failOpen) throw new Error("no streams on this session");
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

  /** The peer or the path killing the session out from under us. */
  rejectClosed(e: unknown) {
    this.#rejectClosed(e);
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

function errorRecorder() {
  const errors: { error: unknown; context: Record<string, string> }[] = [];
  const onError = (error: unknown, context: Record<string, string>) =>
    errors.push({ error, context });
  return { errors, onError };
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

const mint = () =>
  Promise.resolve({ ticket: "tkt", endpoint: "127.0.0.1:4433", certHashB64: undefined });

async function settled(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

beforeEach(() => {
  vi.stubGlobal("WebTransport", FakeWebTransport);
  FakeWebTransport.failDial = false;
  FakeWebTransport.failOpen = false;
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

describe("errors as data", () => {
  it("reports a mint failure under the mint phase, endpoint unknown", async () => {
    const { sink } = sinkRecorder();
    const { errors, onError } = errorRecorder();
    const failingMint = () => Promise.reject(new Error("/session 503"));

    await expect(createTransport({ mint: failingMint, onError }).connect(sink)).rejects.toThrow(
      "/session 503",
    );

    expect(errors).toHaveLength(1);
    expect(messageOf(errors[0]!.error)).toBe("/session 503");
    expect(errors[0]!.context["error.op"]).toBe("transport.dial");
    expect(errors[0]!.context["error.phase"]).toBe("mint");
    expect(errors[0]!.context["error.after_ms"]).toMatch(/^\d+$/);
    expect(errors[0]!.context).not.toHaveProperty("transport.endpoint");
  });

  it("reports a handshake timeout with the endpoint, then rejects", async () => {
    vi.useFakeTimers();
    FakeWebTransport.failDial = true;
    const { sink } = sinkRecorder();
    const { errors, onError } = errorRecorder();

    const pending = createTransport({ mint, onError }).connect(sink);
    const outcome = pending.then(
      () => "connected",
      (e: unknown) => messageOf(e),
    );
    await vi.advanceTimersByTimeAsync(10_001);

    expect(await outcome).toBe("WebTransport handshake timeout (10s)");
    expect(errors).toHaveLength(1);
    expect(messageOf(errors[0]!.error)).toBe("WebTransport handshake timeout (10s)");
    expect(errors[0]!.context["error.op"]).toBe("transport.dial");
    expect(errors[0]!.context["error.phase"]).toBe("handshake");
    expect(errors[0]!.context["transport.endpoint"]).toBe("127.0.0.1:4433");
  });

  it("reports the late rejection of a dial abandoned to the deadline", async () => {
    vi.useFakeTimers();
    FakeWebTransport.failDial = true;
    const { sink } = sinkRecorder();
    const { errors, onError } = errorRecorder();

    const pending = createTransport({ mint, onError }).connect(sink);
    const outcome = pending.then(
      () => "connected",
      (e: unknown) => messageOf(e),
    );
    await vi.advanceTimersByTimeAsync(10_001);
    expect(await outcome).toBe("WebTransport handshake timeout (10s)");

    // The network stack names the real failure after we already gave up.
    FakeWebTransport.last!.rejectReady(new Error("QUIC handshake refused"));
    await vi.advanceTimersByTimeAsync(1);

    const late = errors.filter((r) => r.context["error.phase"] === "handshake-late");
    expect(late).toHaveLength(1);
    expect(messageOf(late[0]!.error)).toBe("QUIC handshake refused");
    expect(late[0]!.context["error.op"]).toBe("transport.dial");
    expect(late[0]!.context["transport.endpoint"]).toBe("127.0.0.1:4433");
  });

  it("reports a stream/writer setup failure under the open phase", async () => {
    FakeWebTransport.failOpen = true;
    const { sink } = sinkRecorder();
    const { errors, onError } = errorRecorder();

    await expect(createTransport({ mint, onError }).connect(sink)).rejects.toThrow(
      "no streams on this session",
    );

    const open = errors.filter((r) => r.context["error.phase"] === "open");
    expect(open).toHaveLength(1);
    expect(messageOf(open[0]!.error)).toBe("no streams on this session");
    expect(open[0]!.context["error.op"]).toBe("transport.dial");
  });

  it("reports the uplink write error that caused a write-error teardown", async () => {
    const { sink, reasons } = sinkRecorder();
    const { errors, onError } = errorRecorder();
    const { connection } = await createTransport({ mint, onError }).connect(sink);
    FakeWebTransport.last!.writeMode = "reject";

    connection.sendFrame(Uint8Array.of(9));
    await settled();
    await settled();

    expect(reasons).toEqual(["write-error"]);
    const uplink = errors.filter((r) => r.context["error.phase"] === "uplink");
    expect(uplink).toHaveLength(1);
    expect(messageOf(uplink[0]!.error)).toBe("path broke");
    expect(uplink[0]!.context["error.op"]).toBe("transport.session");
  });

  it("reports a datagram write rejection while the lane stays up", async () => {
    const { sink, reasons } = sinkRecorder();
    const { errors, onError } = errorRecorder();
    const { connection } = await createTransport({ mint, onError }).connect(sink);
    FakeWebTransport.last!.datagramWriteMode = "reject";

    connection.sendDatagram(Uint8Array.of(1, 2, 3));
    await settled();
    await settled();

    expect(reasons).toEqual([]);
    const datagram = errors.filter((r) => r.context["error.phase"] === "datagram");
    expect(datagram).toHaveLength(1);
    expect(messageOf(datagram[0]!.error)).toBe("datagram lane broke");
    expect(datagram[0]!.context["error.op"]).toBe("transport.session");
  });

  it("reports the peer killing the session, then tears down", async () => {
    const { sink, reasons } = sinkRecorder();
    const { errors, onError } = errorRecorder();
    await createTransport({ mint, onError }).connect(sink);

    FakeWebTransport.last!.rejectClosed(new Error("peer went away"));
    await settled();
    await settled();

    expect(reasons).toEqual(["transport"]);
    const closed = errors.filter((r) => r.context["error.phase"] === "closed");
    expect(closed).toHaveLength(1);
    expect(messageOf(closed[0]!.error)).toBe("peer went away");
    expect(closed[0]!.context["error.op"]).toBe("transport.session");
  });

  it("reports nothing for a self-initiated close", async () => {
    const { sink, reasons } = sinkRecorder();
    const { errors, onError } = errorRecorder();
    const { connection } = await createTransport({ mint, onError }).connect(sink);

    connection.close();
    await settled();
    await settled();

    expect(reasons).toEqual(["transport"]);
    expect(errors).toEqual([]);
  });

  it("connects and tears down the same without a handler", async () => {
    const { sink, reasons } = sinkRecorder();
    const { connection } = await createTransport({ mint }).connect(sink);
    FakeWebTransport.last!.writeMode = "reject";

    connection.sendFrame(Uint8Array.of(9));
    await settled();
    await settled();

    expect(reasons).toEqual(["write-error"]);
  });
});

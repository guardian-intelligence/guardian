// The browser's WebTransport as the core's transport port. A dial is two
// steps: the app's mint closure obtains an admission ticket and the
// endpoint (auth is the app's business), and the QUIC dial goes direct to
// that endpoint — pinned by certificate hash while the gateway serves a
// self-signed cert.
//
// Exactly one dial per call, success or throw. Backoff, redial and the
// decision to give up belong to the core.
//
// The uplink is honest: sends enqueue into a bounded queue drained by one
// writer that awaits readiness, so a write failure or a hung path tears
// the connection down through `onClosed(reason)` instead of vanishing
// into a swallowed promise (the transport this replaces did exactly
// that). Loss on the datagram lane remains the lane's contract.

import type {
  CloseReason,
  Connection,
  ConnectionSink,
  Dialed,
  TransportPort,
} from "@guardian/chunkies";

const DIAL_TIMEOUT_MS = 10_000;

/** An uplink write pending longer than this means the path is dead in a
 * way `closed` may never report. */
const STALL_MS = 5_000;

/** Bounded uplink queue: 8x the session core's 4KiB frame scratch. A
 * legitimate burst fits; a stalled path overflows into teardown. */
const QUEUE_MAX_BYTES = 32 * 1024;

/** The floor every QUIC path guarantees; used when the browser cannot
 * name its datagram size yet. */
const DATAGRAM_FLOOR = 1_200;

/** What the app's mint step must produce for one dial. */
export type Minted = {
  /** The opaque admission ticket, as minted. */
  readonly ticket: string;
  /** "host:port" the WebTransport dial goes to, directly. */
  readonly endpoint: string;
  /** Present while the endpoint serves a self-signed certificate. */
  readonly certHashB64?: string | undefined;
};

export type TransportOptions = {
  /** Mints admission for one dial. Called fresh per dial: tickets and
   * tokens age out mid-session. Rejection fails the dial. */
  readonly mint: () => Promise<Minted>;
  /** A dial that reached the gateway, for the connected span. */
  readonly onDialed?: ((dialMs: number) => void) | undefined;
  /** Stream and datagram bytes as they arrive: the headline downlink
   * cost metric. */
  readonly onBytesDown?: ((n: number) => void) | undefined;
};

export function createTransport(options: TransportOptions): TransportPort {
  return {
    async connect(sink: ConnectionSink): Promise<Dialed> {
      const dialStart = performance.now();
      const minted = await options.mint();

      const transport = new WebTransport(
        `https://${minted.endpoint}/wt`,
        minted.certHashB64
          ? {
              serverCertificateHashes: [
                { algorithm: "sha-256", value: fromBase64(minted.certHashB64) },
              ],
            }
          : undefined,
      );

      // A blocked UDP path can leave the handshake pending forever — ready
      // neither resolves nor rejects and closed never settles — so nothing
      // downstream would ever run or report. Race it against a deadline.
      let dialTimer: ReturnType<typeof setTimeout> | undefined;
      try {
        await Promise.race([
          transport.ready,
          new Promise<never>((_, reject) => {
            dialTimer = setTimeout(
              () =>
                reject(new Error(`WebTransport handshake timeout (${DIAL_TIMEOUT_MS / 1000}s)`)),
              DIAL_TIMEOUT_MS,
            );
          }),
        ]);
      } catch (e) {
        closeQuietly(transport);
        throw e;
      } finally {
        clearTimeout(dialTimer);
      }

      let stream;
      let streamWriter: WritableStreamDefaultWriter<Uint8Array>;
      let datagramWriter: WritableStreamDefaultWriter<Uint8Array>;
      try {
        stream = await transport.createBidirectionalStream();
        streamWriter = stream.writable.getWriter();
        datagramWriter = transport.datagrams.writable.getWriter();
      } catch (e) {
        closeQuietly(transport);
        throw e;
      }

      options.onDialed?.(Math.round(performance.now() - dialStart));

      // onClosed must fire exactly once whatever races: our teardown, the
      // peer closing, or the path idling out.
      let closed = false;
      const closeOnce = (reason: CloseReason) => {
        if (closed) return;
        closed = true;
        closeQuietly(transport);
        sink.onClosed(reason);
      };

      // ---- the honest uplink ----
      const queue: Uint8Array[] = [];
      let queuedBytes = 0;
      let draining = false;
      let stallTimer: ReturnType<typeof setTimeout> | undefined;

      const drain = async () => {
        if (draining) return;
        draining = true;
        try {
          while (queue.length > 0 && !closed) {
            const frame = queue[0] as Uint8Array;
            stallTimer = setTimeout(() => closeOnce("stall"), STALL_MS);
            try {
              await streamWriter.ready;
              await streamWriter.write(frame);
            } finally {
              clearTimeout(stallTimer);
              stallTimer = undefined;
            }
            queue.shift();
            queuedBytes -= frame.byteLength;
          }
        } catch {
          closeOnce("write-error");
        } finally {
          draining = false;
        }
      };

      const connection: Connection = {
        sendFrame: (bytes) => {
          if (closed) return;
          if (queuedBytes + bytes.byteLength > QUEUE_MAX_BYTES) {
            // The path cannot keep up with the core's own send rate: dead.
            closeOnce("stall");
            return;
          }
          queue.push(bytes);
          queuedBytes += bytes.byteLength;
          void drain();
        },
        sendDatagram: (bytes) => {
          if (closed) return;
          if (bytes.byteLength > connection.datagramBudget()) return;
          void datagramWriter.write(bytes).catch(() => {
            // The datagram lane is lossy by contract; a failed write is
            // loss, not death. Stream writes are where death is detected.
          });
        },
        datagramBudget: () => transport.datagrams.maxDatagramSize || DATAGRAM_FLOOR,
        close: () => closeOnce("transport"),
      };

      void deliver(stream.readable, (bytes) => {
        options.onBytesDown?.(bytes.length);
        sink.onStreamBytes(bytes);
      });
      void deliver(transport.datagrams.readable, (bytes) => {
        options.onBytesDown?.(bytes.length);
        sink.onDatagram(bytes);
      });
      transport.closed.catch(() => {}).finally(() => closeOnce("transport"));

      return { connection, ticket: new TextEncoder().encode(minted.ticket) };
    },
  };
}

async function deliver(readable: ReadableStream, to: (bytes: Uint8Array) => void): Promise<void> {
  // The DOM lib leaves WebTransport's streams unparameterized; the spec
  // says every chunk is a Uint8Array.
  const reader = (readable as ReadableStream<Uint8Array>).getReader();
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) return;
      if (value) to(value);
    }
  } catch {
    // Torn down. `closed` settling is what redials.
  }
}

function closeQuietly(transport: WebTransport): void {
  try {
    transport.close();
  } catch {
    // already gone
  }
}

function fromBase64(s: string): Uint8Array<ArrayBuffer> {
  return Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
}

// The browser's WebTransport as the core's transport port. A dial is two
// steps: POST /session mints an admission ticket over the
// Cloudflare-proxied ingress, and the QUIC dial goes direct to the
// endpoint that response names — pinned by the certificate hash it
// carries while the park serves a self-signed cert.
//
// Exactly one dial per call, success or throw. Backoff, redial and the
// decision to give up belong to the core.

import type {
  Connection,
  ConnectionSink,
  Dialed,
  TransportPort,
} from "@guardian/mythrad-client-core";
import * as v from "valibot";

const DIAL_TIMEOUT_MS = 10_000;

const Session = v.object({
  ticket: v.string(),
  endpoint: v.string(),
  certHashB64: v.optional(v.string()),
});

/** Who is dialing, resolved fresh per dial: a token can age out mid-session. */
export type Credentials = {
  /** null means anonymous, which the mint only ever grants a spectator. */
  readonly token: string | null;
  readonly device: string;
  /** A signed-in player who asked to watch instead. */
  readonly spectate: boolean;
};

export type TransportOptions = {
  readonly park: string;
  readonly credentials: () => Promise<Credentials>;
  /** A dial that reached the park, for the connected span. */
  readonly onDialed: (dialMs: number, anon: boolean) => void;
};

export function createTransport(options: TransportOptions): TransportPort {
  return {
    async connect(sink: ConnectionSink): Promise<Dialed> {
      const creds = await options.credentials();
      const anon = creds.token === null;
      const q = new URLSearchParams({ park: options.park });
      const headers: Record<string, string> = {};
      if (anon) {
        q.set("spectate", "1");
        q.set("device", creds.device);
      } else {
        headers.Authorization = `Bearer ${creds.token}`;
        if (creds.spectate) q.set("spectate", "1");
      }

      const dialStart = performance.now();
      const mint = await fetch(`/session?${q}`, { method: "POST", headers });
      if (!mint.ok) throw new Error(`/session ${mint.status}`);
      const session = v.parse(Session, await mint.json());

      const transport = new WebTransport(
        `https://${session.endpoint}/wt`,
        session.certHashB64
          ? {
              serverCertificateHashes: [
                { algorithm: "sha-256", value: fromBase64(session.certHashB64) },
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
        close(transport);
        throw e;
      } finally {
        clearTimeout(dialTimer);
      }

      let stream;
      let streamWriter;
      let datagramWriter;
      try {
        stream = await transport.createBidirectionalStream();
        streamWriter = stream.writable.getWriter();
        datagramWriter = transport.datagrams.writable.getWriter();
      } catch (e) {
        close(transport);
        throw e;
      }

      options.onDialed(Math.round(performance.now() - dialStart), anon);
      void deliver(stream.readable, (bytes) => sink.onStreamBytes(bytes));
      void deliver(transport.datagrams.readable, (bytes) => sink.onDatagram(bytes));
      transport.closed.catch(() => {}).finally(() => sink.onClosed());

      const connection: Connection = {
        sendStream: (bytes) => {
          void streamWriter.write(bytes).catch(() => {});
        },
        sendDatagram: (bytes) => {
          void datagramWriter.write(bytes).catch(() => {});
        },
        close: () => close(transport),
      };
      return { connection, ticket: new TextEncoder().encode(session.ticket) };
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

function close(transport: WebTransport): void {
  try {
    transport.close();
  } catch {
    // already gone
  }
}

function fromBase64(s: string): Uint8Array<ArrayBuffer> {
  return Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
}

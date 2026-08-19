// The app's slice of the transport: minting admission. POST /session goes
// over the Cloudflare-proxied ingress with the app's credentials; the
// lanes, the honest uplink, and the QUIC dial live in
// @guardian/chunkies-transport-web.

import type { TransportPort } from "@guardian/chunkies";
import { createTransport as createWebTransportPort } from "@guardian/chunkies-transport-web";
import { reportError } from "@guardian/telemetry";
import * as v from "valibot";

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
  /** Stream and datagram bytes as they arrive: the headline downlink cost metric. */
  readonly onBytesDown: (n: number) => void;
};

export function createTransport(options: TransportOptions): TransportPort {
  let anon = false;
  return createWebTransportPort({
    mint: async () => {
      const creds = await options.credentials();
      anon = creds.token === null;
      // The gateway speaks framework vocabulary: WUM's park is its chunk.
      const q = new URLSearchParams({ chunk: options.park });
      const headers: Record<string, string> = {};
      if (anon) {
        q.set("spectate", "1");
        q.set("device", creds.device);
      } else {
        headers.Authorization = `Bearer ${creds.token}`;
        if (creds.spectate) q.set("spectate", "1");
      }
      const mint = await fetch(`/session?${q}`, { method: "POST", headers });
      if (!mint.ok) throw new Error(`/session ${mint.status}`);
      return v.parse(Session, await mint.json());
    },
    onDialed: (dialMs) => options.onDialed(dialMs, anon),
    onBytesDown: options.onBytesDown,
    // Every error the transport absorbs — dial phases, session death, even
    // late rejections of abandoned dials — lands in the general error lane
    // with its phase context; the analytics side filters on error.op.
    onError: (e, context) => void reportError(e, { ...context, "wum.park": options.park }),
  });
}

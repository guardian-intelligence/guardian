// Everything the host needs from the platform it runs on, as injected
// interfaces. The browser supplies WebTransport, fetch and crypto; a
// native shell supplies its own; a test supplies fakes and gets a
// deterministic session. Nothing here has an implementation — the host is
// the only consumer, and each app writes its own adapters.
//
// Auth is deliberately absent. Minting a session and obtaining the ticket
// is the app's business; the host sees the ticket as opaque bytes handed
// back from `connect()`.

/** A live connection to the authority. Every method is fire-and-forget. */
export interface Connection {
  /** Writes one whole frame to the bidirectional stream. */
  sendStream(bytes: Uint8Array): void;
  /** Writes one datagram. Loss is expected and unreported. */
  sendDatagram(bytes: Uint8Array): void;
  /** Tears the connection down. Idempotent. */
  close(): void;
}

/** Callbacks a connection drives. The host installs these before dialing. */
export interface ConnectionSink {
  /** A stream read: any number of bytes, at any frame boundary or none. */
  onStreamBytes(bytes: Uint8Array): void;
  /** One datagram, whole. */
  onDatagram(bytes: Uint8Array): void;
  /** The connection is gone. Called exactly once per successful connect. */
  onClosed(): void;
}

/**
 * The result of a successful dial. `ticket` is the opaque authorization
 * blob the app minted; the host copies it into the session core's staging
 * buffer and the core builds the hello frame around it.
 */
export interface Dialed {
  readonly connection: Connection;
  readonly ticket: Uint8Array;
}

export interface TransportPort {
  /**
   * Dials the authority. Rejects on any failure, including a handshake
   * that never settles — the host's backoff loop treats a rejection and a
   * later `onClosed` identically.
   */
  connect(sink: ConnectionSink): Promise<Dialed>;
}

/** Which wasm module to fetch. `replica` is the sim; `session` carries the session core. */
export type ModuleSlot = "session" | "replica";

export interface Ports {
  readonly transport: TransportPort;
  /**
   * Fetches module bytes. `ref` is the cache-busting reference the app
   * resolved at boot; a module swap passes none, meaning "whatever is
   * current".
   */
  fetchModule(slot: ModuleSlot, ref?: string): Promise<ArrayBuffer>;
  /**
   * Fetches a content-addressed blob the session core asked for, by the
   * numeric kind its request verb named and the 16-hex-char id.
   */
  fetchBlob(kind: number, id: string): Promise<ArrayBuffer>;
  /**
   * A fresh, cryptographically random 32 bits per session, minted once and
   * handed to `session_init` as the intent-id nonce. Use
   * `crypto.getRandomValues` and the full width; `Math.random` is banned
   * repo-wide in deterministic paths and is not acceptable here.
   *
   * This is a correctness requirement, not hygiene. Intent ids are
   * `(nonce << 32) | counter`, and proto 4 dropped `actor` from the event
   * frame, so the id is the ONLY handle matching an ack back to the intent
   * that earned it. A nonce repeated across page loads for the same
   * subject breaks that twice over: the server's idempotency window spans
   * reconnects, so a replayed id is swallowed as a resend — silently
   * eating a join — and any ack that does arrive can be attributed to the
   * wrong load's pending intent.
   *
   * A deterministic test may seed this, which is the point of injecting
   * it; a shipping host may not.
   */
  random32(): number;
}

/**
 * Codes the HOST mints, for facts the session core cannot know. They
 * start at 1000 so a consumer can tell them apart from the core's 1..22
 * by magnitude alone, and so neither range can ever grow into the other.
 */
export const HostEmit = {
  /**
   * redial(wait_ms, consecutive_failures). The backoff loop is the host's,
   * so the wait it chose is not visible to the core and would otherwise
   * only reach a log line.
   */
  redial: 1000,
  /**
   * teardown(reason, 0). The core raises teardown via a request verb, not
   * an emit, so without this the reason (ResyncReason.streamOverflow or
   * .framing) would reach a log line but never analytics — and a redial
   * storm from lost framing would be indistinguishable from ordinary
   * backoff on a dashboard.
   */
  teardown: 1001,
} as const;

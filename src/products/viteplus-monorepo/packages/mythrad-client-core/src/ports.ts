// Everything the core needs from the platform it runs on, as injected
// interfaces. The browser supplies WebTransport, fetch, Date.now and
// crypto; a native shell supplies its own; a test supplies fakes and gets
// a deterministic session. Nothing here has an implementation — the core
// is the only consumer, and each host writes its own adapters.
//
// Auth is deliberately absent. Minting a session and obtaining the ticket
// is the app's business; the core sees the ticket as opaque bytes handed
// back from `connect()`.

/** A live connection to a park. Every method is fire-and-forget. */
export interface Connection {
  /** Writes one whole frame to the bidirectional stream. */
  sendStream(bytes: Uint8Array): void;
  /** Writes one datagram. Loss is expected and unreported. */
  sendDatagram(bytes: Uint8Array): void;
  /** Tears the connection down. Idempotent. */
  close(): void;
}

/** Callbacks a connection drives. The core installs these before dialing. */
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
 * blob the app minted; the core copies it into the session core's staging
 * buffer and the core builds the hello frame around it.
 */
export interface Dialed {
  readonly connection: Connection;
  readonly ticket: Uint8Array;
}

export interface TransportPort {
  /**
   * Dials the park. Rejects on any failure, including a handshake that
   * never settles — the core's backoff loop treats a rejection and a
   * later `onClosed` identically.
   */
  connect(sink: ConnectionSink): Promise<Dialed>;
}

/** Which wasm module to fetch. `park` is the sim; `client` carries the session core. */
export type BehaviorModule = "park" | "client";

export interface Ports {
  readonly transport: TransportPort;
  /**
   * Fetches module bytes. `ref` is the cache-busting reference the app
   * read from `/wt-info`; a module swap passes none, meaning "whatever is
   * current".
   */
  fetchBehavior(kind: BehaviorModule, ref?: string): Promise<ArrayBuffer>;
  /** Fetches a terrain artifact by its 16-hex-char blob id. */
  fetchTerrain(idHex: string): Promise<ArrayBuffer>;
  /** Wall clock in milliseconds. The session core's only notion of time. */
  now(): number;
  /** The session core's numeric telemetry vocabulary (design doc §Telemetry). */
  telemetry(code: number, a: bigint, b: bigint): void;
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
  /** SHA-256, for identifying a fetched module. Defaults to `crypto.subtle`. */
  sha256?(bytes: ArrayBuffer): Promise<ArrayBuffer>;
  /** Human-readable trace of session events. Defaults to a no-op. */
  log?(line: string): void;
}

/**
 * Telemetry codes the session core emits, 1..16. The numbers are the
 * contract and are shared with the Rust crate; host-minted codes live in
 * `HostEmit` and start at 1000 so the two can never be confused in a
 * dashboard or a span attribute.
 */
export const Emit = {
  connectedHelloSent: 1,
  welcome: 2,
  eventApplied: 3,
  rollback: 4,
  resyncRequested: 5,
  snapshotRestored: 6,
  checkSent: 7,
  verdict: 8,
  mismatch: 9,
  reject: 10,
  moduleSwapWanted: 11,
  moduleSwapped: 12,
  clockState: 13,
  intentSent: 14,
  autoRejoin: 15,
  /** restore_failed(park code, seq). Code 4 is wrong terrain and is not retried. */
  restoreFailed: 16,
} as const;

/**
 * Codes the HOST mints, for facts the session core cannot know. They
 * start at 1000 so a consumer can tell them apart from the core's 1..16
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

/**
 * Why the core gave up on the state it had. One numbering across both
 * recovery verbs, and after the teardown split no code appears on both:
 * 1..10 only ever arrive as `request(3)` resync_wanted, 11..12 only ever
 * as `request(4)` teardown. A consumer can therefore key severity off the
 * verb rather than off the code. The host only reports these.
 */
export const ResyncReason = {
  clock: 1,
  lateEvent: 2,
  eventRejected: 3,
  hashMismatch: 4,
  checkAgedOut: 5,
  moduleEpoch: 6,
  terrainFetch: 7,
  restoreFailed: 8,
  queueOverflow: 9,
  moduleSwapped: 10,
  /** Reassembly ran out of room. Arrives as a teardown, never a resync. */
  streamOverflow: 11,
  /** A length prefix that cannot be read. Also a teardown. */
  framing: 12,
} as const;

/**
 * Fixed capacities in the session core. Exceeding one is not an error the
 * host sees — the core drops to a resync — but a host that batches
 * outbound work needs to know they exist.
 */
export const Caps = {
  /** Intents in flight before the oldest is dropped. */
  intents: 32,
  /** Events held awaiting their turn in seq order. */
  queuedEvents: 256,
  /** Events retained for rollback replay. */
  recentEvents: 512,
  /** Largest sim event payload the core will carry. */
  eventPayload: 64,
  /**
   * Stream reassembly. The trigger is what is ALREADY buffered plus the
   * incoming read, not the size of any one frame — and it cannot be one
   * frame, because a park's whole state fits in its 64 KiB io buffer, so
   * the largest snapshot frame it can emit is around 61.5 KB. Overflow
   * takes a partial frame sitting in the buffer with a further large read
   * arriving behind it.
   *
   * When it trips, the core drops the whole buffer and raises
   * `Request.teardown`: byte alignment is lost, there is no way to find
   * the next frame boundary in what remains, and a resync frame written
   * onto that stream would be answered into the same garbage.
   */
  reassembly: 160 * 1024,
  /** Outbound frame buffer, which bounds the ticket. */
  outbound: 4 * 1024,
} as const;

/** `request(kind, a)` codes the session core raises to the host. */
export const Request = {
  needTerrain: 1,
  needModule: 2,
  resyncWanted: 3,
  /**
   * The stream's byte alignment is gone and nothing sent on it can get
   * that back. The host MUST close the transport: logging and carrying on
   * reproduces the wedge with a nicer log line, because every subsequent
   * byte on that stream is unparseable. The redial is the repair.
   */
  teardown: 4,
} as const;

/** `session_stat(kind)` counters. 7 (bytes_down) is host-side and lives on the core. */
export const Stat = {
  events: 1,
  rollbacks: 2,
  resyncs: 3,
  checks: 4,
  mismatches: 5,
  rejects: 6,
} as const;

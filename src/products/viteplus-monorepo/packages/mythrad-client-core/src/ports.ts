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

// The Rust-owned numeric vocabularies. Generated from the session crate
// (each emit's slot semantics are documented there, on the `T_*` consts)
// so no hand-mirrored table can drift; every code the core can emit has a
// name here, and the suite fails when one arrives that does not.
export {
  Emit,
  IntentDrop,
  Request,
  ResyncReason,
  type EmitCode,
  type IntentDropCode,
  type RequestCode,
  type ResyncReasonCode,
} from "./telemetry.gen.ts";

/**
 * The intent kinds a host can send, named for dashboards and panes. The
 * numbers are the park's event kinds — an action's kind rides its spans
 * as this number, so a NEW action needs no telemetry change, only a row
 * here; the conformance suite fails when the core sends a kind this
 * table cannot name.
 */
export const ActionKind: Record<number, string> = {
  1: "join",
  3: "check_in",
  4: "move_to",
  8: "boost",
};

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

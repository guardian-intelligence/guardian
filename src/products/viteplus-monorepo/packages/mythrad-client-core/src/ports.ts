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
 * Telemetry codes the session core emits, 1..22. The numbers are the
 * contract and are shared with the Rust crate; host-minted codes live in
 * `HostEmit` and start at 1000 so the two can never be confused in a
 * dashboard or a span attribute.
 *
 * Every code the core can emit has to be named here. A number the host
 * cannot name reaches a dashboard as a bare integer and a reader as
 * nothing at all, and the suite has a case that fails when one arrives —
 * this list going stale is otherwise completely silent.
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
  /** presence(source, tick): the journal placed our dog, or the park said it already had it. */
  presence: 17,
  /**
   * replayed(seq, tick): one per event a repair re-applies, emitted BEFORE
   * the attempt so an event the park then refuses is still named. Without
   * it a repair can only be paired with the events that happened to be
   * near it in time rather than the ones it actually touched.
   */
  replayed: 18,
  /**
   * arrived(event tick, replica tick): where the replica stood the moment
   * an event was accepted into the queue, before anything was done with
   * it. The MARGIN is `a - b`: positive means the event beat the replica
   * to its own tick, negative means it was already late and a repair is
   * owed. Both are raw absolute ticks because the slots are unsigned and a
   * negative margin could not be carried — the subtraction, and its sign,
   * belong to the reader.
   *
   * With the replica's measured trail this is what turns the authority's
   * stamp-to-wire delay from an estimate into a number: delay = trail
   * minus margin.
   */
  eventArrived: 19,
  /**
   * answered((resends << 16) | kind, latency_ms): the journal applied an
   * event carrying one of OUR intent ids — the player's action became
   * world state. The latency is a finished fact the core measured, from
   * the action's first wire write to this apply, so the host does zero
   * bookkeeping: name the kind, forward, done. Every host on every
   * platform reports the same figure the same way.
   */
  intentAnswered: 20,
  /**
   * resent(kind, resends): a pending action went back on the wire under
   * its original id (the authority dedupes, so a resend can never
   * double-apply). The answered fact carries the final count; this marks
   * the retry itself.
   */
  intentResent: 21,
  /**
   * dropped((reason << 16) | kind, held_ms): a pending action was
   * discarded and will never be answered — the player acted and the
   * world will not reflect it. Reasons in `IntentDrop`; held_ms is how
   * long it had waited (0 for one that never reached the wire). Nothing
   * else says so: no reject arrives and no event applies.
   */
  intentDropped: 22,
} as const;

/** The high half of `intentDropped`'s `a` slot. */
export const IntentDrop = {
  /** A 33rd in-flight intent evicted the oldest. */
  overflow: 1,
  /** `reidentify`: the old identity's intents die with it. */
  reidentify: 2,
} as const;

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

/**
 * Why the core gave up on the state it had. One numbering across both
 * recovery verbs: 1..10 and 13 only ever arrive as `request(3)`
 * resync_wanted, 11..12 only ever as `request(4)` teardown. A consumer can
 * therefore key severity off the verb rather than off the code. The host
 * only reports these.
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
  /**
   * A repair replayed an event the park refused. The world is then quietly
   * missing it — no seq gap, no late event, nothing else to find it by —
   * so the only honest recovery is a snapshot.
   */
  replayRefused: 13,
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

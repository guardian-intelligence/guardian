// The host. It owns two wasm instances — the replica module carrying the
// world and the session module carrying the session core — and does
// exactly four things: copies bytes between their linear memories on the
// replica verbs, routes frames to and from the transport, performs the
// platform errands the core asks for (fetch a blob, fetch a module,
// inflate), and opens two guarded doors — `extension` and
// `readProjection` — through which a game layer reaches the exports the
// framework does not know.
//
// All protocol behavior — ordering, rollback, resync, strikes, intent
// identity, check cadence — lives in the session core. If a decision
// about the protocol is being made in this file, it is in the wrong
// place. If a game rule is readable in this file, it is in the wrong
// package.

// fflate's `inflateSync` is the no-wrapper (raw DEFLATE) variant, which
// is what a snapshot frame carries.
import { inflateSync } from "fflate";
import {
  ABI_VERSION,
  abiViolations,
  HOST_IMPORTS,
  REPLICA_EXPORTS,
  SESSION_EXPORTS,
  type HostImports,
  type ReplicaExports,
  type SessionExports,
} from "./abi.ts";
import { DIAG_BYTES, decodeDiag, type Diag } from "./diag.ts";
import { hex64, moduleWordHex, Role, type RoleName } from "./ids.ts";
import { HostEmit, type Connection, type Ports } from "./ports.ts";
import { decodePumpStatus, decodeWelcomeEmit, type HostState, type PumpStatus } from "./status.ts";
import { Emit, Request } from "./telemetry.gen.ts";

const BACKOFF_MIN_MS = 300;
const BACKOFF_MAX_MS = 5000;
const BACKOFF_JITTER_MS = 250;
/** Frame CPU the clock may spend stepping per pump. */
const DEFAULT_STEP_BUDGET_US = 8000;
/** How long a failed module fetch waits before trying again. */
const MODULE_RETRY_MS = 3000;
/** Straight dial failures before the phase turns `unreachable`. */
const UNREACHABLE_AFTER_DIALS = 3;

export type HostOptions = {
  /** The actor this session speaks for. Spectators still pass one; projections may key on it. */
  readonly actorId?: bigint;
  readonly role?: RoleName;
  /** Check cadence in milliseconds, handed to `session_init`. */
  readonly checkMs?: number;
  /** Cache-busting references the app resolved at boot, per module slot. */
  readonly moduleRefs?: { readonly session?: string; readonly replica?: string };
  /** Wall clock in milliseconds: the session core's only notion of time. */
  readonly now?: () => number;
  /**
   * Timer injection. A deterministic test drives the redial and retry
   * lanes through its own scheduler; the default is `setTimeout`.
   */
  readonly schedule?: (fn: () => void, ms: number) => () => void;
  /** The session core's numeric telemetry vocabulary, plus the `HostEmit` codes. */
  readonly telemetry?: (code: number, a: bigint, b: bigint) => void;
  /** SHA-256, for identifying a fetched module. Defaults to `crypto.subtle`. */
  readonly sha256?: (bytes: ArrayBuffer) => Promise<ArrayBuffer>;
  /** Human-readable trace of session events. Defaults to a no-op. */
  readonly log?: (line: string) => void;
  /** Default `pump` step budget in microseconds of frame CPU. */
  readonly stepBudgetUs?: number;
};

/**
 * One frame's smoothing door, wrapping the session module's
 * `frame_buf`/`frame_cap`/`smooth_frame` batch surface.
 */
export type FrameQuads = {
  readonly nowMs: number;
  /** Q16 interpolation alpha between ticks, from the session's own clock. */
  readonly phaseQ16: number;
  /**
   * Runs the smoother over `quads` in place: four Q16.16 ints per entity —
   * previous x, previous y, current x, current y — with the presented
   * position written back into the first two, batched by `frame_cap`.
   */
  readonly smooth: (quads: Int32Array, snapQ16: number) => void;
};

type DynamicExports = { readonly [exportName: string]: unknown };

const defaultSchedule = (fn: () => void, ms: number): (() => void) => {
  const t = setTimeout(fn, ms);
  return () => clearTimeout(t);
};

export class ReplicaHost {
  readonly #ports: Ports;
  readonly #now: () => number;
  readonly #schedule: (fn: () => void, ms: number) => () => void;
  readonly #telemetry: (code: number, a: bigint, b: bigint) => void;
  readonly #sha256: (bytes: ArrayBuffer) => Promise<ArrayBuffer>;
  readonly #logLine: ((line: string) => void) | null;
  readonly #checkMs: number;
  readonly #stepBudgetUs: number;
  readonly #moduleRefs: { readonly session?: string; readonly replica?: string };

  #session: SessionExports | null = null;
  /** The world. One instance: the journal is what a projection reads. */
  #replica: ReplicaExports | null = null;

  /** The actor projections key on. Follows `reidentify`, so it is not read from options. */
  #actorId: bigint;

  #blob: Uint8Array | null = null;
  #blobId = 0n;

  #conn: Connection | null = null;
  #dialEpoch = 0;
  #backoffMs = BACKOFF_MIN_MS;
  #cancelRedial: (() => void) | null = null;
  #disposed = false;

  // Blob loads run one at a time and in request order: the core can ask
  // for blob B while A is still in flight (a snapshot from a world that
  // re-based), and answering `ready` out of order would leave it waiting
  // on a load that already reported.
  #blobQueue: Promise<void> = Promise.resolve();
  #fetchingModule = false;
  /** Cancels the module-swap retry, so a disposed host stops instantiating modules. */
  #cancelModuleRetry: (() => void) | null = null;

  /** A teardown raised from inside a session call, run once that call returns. */
  #pendingTeardown: number | null = null;
  /**
   * True while a host-initiated wasm call is on the stack. The session
   * module hands out one `&mut Session` from a static, so a call entering
   * it from inside an emit or request callback would alias it — both
   * doors refuse instead.
   */
  #inCall = false;
  readonly #extensionCache = new Map<string, (...args: unknown[]) => unknown>();
  readonly #extensionDoor: object;

  #state: HostState;
  readonly #subscribers = new Set<(s: HostState) => void>();

  readonly #diagScratch = new Uint8Array(DIAG_BYTES);

  constructor(ports: Ports, options: HostOptions = {}) {
    this.#ports = ports;
    this.#now = options.now ?? Date.now;
    this.#schedule = options.schedule ?? defaultSchedule;
    this.#telemetry = options.telemetry ?? (() => {});
    this.#sha256 = options.sha256 ?? ((bytes) => crypto.subtle.digest("SHA-256", bytes));
    this.#logLine = options.log ?? null;
    this.#checkMs = options.checkMs ?? 5000;
    this.#stepBudgetUs = options.stepBudgetUs ?? DEFAULT_STEP_BUDGET_US;
    this.#moduleRefs = options.moduleRefs ?? {};
    this.#actorId = options.actorId ?? 0n;
    this.#state = {
      phase: "idle",
      dialAttempts: 0,
      epoch: 0,
      replicaModuleWord: "",
      rateHz: null,
      role: options.role ?? "spectator",
    };
    this.#extensionDoor = new Proxy(
      {},
      {
        get: (_, name) => (typeof name === "string" ? this.#extensionCall(name) : undefined),
      },
    );
  }

  // -------------------------------------------------------------------------
  // Boot
  // -------------------------------------------------------------------------

  /**
   * Instantiates the modules and dials. The caller has already resolved
   * identity and, if it needs them, the module references — this does no
   * auth and no service discovery.
   */
  async boot(): Promise<void> {
    this.#patch({ phase: "booting" });
    const [replicaBytes, sessionBytes] = await Promise.all([
      this.#ports.fetchModule("replica", this.#moduleRefs.replica),
      this.#ports.fetchModule("session", this.#moduleRefs.session),
    ]);
    // Verified before the casts below make any name reachable: a module
    // that fails here is a refused boot with a nameable cause, not a
    // TypeError on every frame of a session already underway.
    const sessionModule = await WebAssembly.compile(sessionBytes);
    refuseAbiDrift("session module", sessionModule, {
      exports: SESSION_EXPORTS,
      imports: HOST_IMPORTS,
    });
    const imports = { host: this.#hostImports() } as unknown as WebAssembly.Imports;
    const session = (await WebAssembly.instantiate(sessionModule, imports))
      .exports as unknown as SessionExports;
    refuseAbiVersion("session module", session.abi_version());
    this.#session = session;

    const word = await this.#wordOf(replicaBytes);
    this.#replica = await instantiateReplica(replicaBytes);

    session.session_init(
      this.#actorId,
      Role[this.#state.role ?? "spectator"],
      this.#checkMs,
      this.#ports.random32() >>> 0,
      BigInt(this.#now()),
    );
    session.session_set_visible(1);
    // Seeds the core's notion of which module the world is running, so the
    // first verdict compares against something. Safe here and nowhere
    // later: no world is live yet, so it has no state to disturb.
    session.session_module_swapped(word);
    this.#patch({ replicaModuleWord: moduleWordHex(word) });
    this.#log(`replica module ${moduleWordHex(word)} live`);
    void this.#connect();
  }

  /** Stops redialing and drops the current connection. The instances stay usable. */
  dispose(): void {
    this.#disposed = true;
    this.#dialEpoch++;
    this.#cancelRedial?.();
    this.#cancelRedial = null;
    // Without this a disposed host still instantiates two wasm modules
    // three seconds later, on a session nothing is watching.
    this.#cancelModuleRetry?.();
    this.#cancelModuleRetry = null;
    this.#closeConnection();
    this.#patch({ phase: "dead" });
  }

  // -------------------------------------------------------------------------
  // Connection lifecycle
  // -------------------------------------------------------------------------

  async #connect(): Promise<void> {
    if (this.#disposed) return;
    const session = this.#session;
    if (!session) throw new Error("connect before boot");
    const myEpoch = ++this.#dialEpoch;
    this.#patch({ phase: this.#dialPhase() });
    try {
      const dialed = await this.#ports.transport.connect({
        onStreamBytes: (b) => {
          if (myEpoch === this.#dialEpoch) this.#onStreamBytes(b);
        },
        onDatagram: (b) => {
          if (myEpoch === this.#dialEpoch) this.#onDatagram(b);
        },
        onClosed: () => {
          if (myEpoch === this.#dialEpoch) this.#onDead();
        },
      });
      if (myEpoch !== this.#dialEpoch) {
        dialed.connection.close();
        return;
      }
      this.#conn = dialed.connection;
      this.#backoffMs = BACKOFF_MIN_MS;
      this.#patch({ phase: "live", dialAttempts: 0 });

      const ticket = dialed.ticket;
      if (ticket.length > session.session_cap()) {
        throw new Error(`ticket is ${ticket.length}B, over the session buffer`);
      }
      new Uint8Array(session.memory.buffer).set(ticket, session.session_buf());
      const code = this.#call(() => session.session_connected(ticket.length, BigInt(this.#now())));
      if (code !== 0) throw new Error(`hello refused (code ${code}); ticket may be too large`);
    } catch (e) {
      this.#log(`connect failed: ${messageOf(e)}`);
      this.#patch({ dialAttempts: this.#state.dialAttempts + 1 });
      if (myEpoch === this.#dialEpoch) this.#onDead();
    }
  }

  #dialPhase(): HostState["phase"] {
    return this.#state.dialAttempts >= UNREACHABLE_AFTER_DIALS ? "unreachable" : "connecting";
  }

  #onDead(): void {
    if (this.#disposed) return;
    this.#closeConnection();
    const wait = this.#backoffMs;
    this.#backoffMs = Math.min(this.#backoffMs * 2, BACKOFF_MAX_MS);
    this.#patch({ phase: this.#dialPhase() });
    // Jitter comes from the injected randomness, so a deterministic run
    // reproduces the whole redial schedule.
    const jitter = ((this.#ports.random32() >>> 0) / 0x1_0000_0000) * BACKOFF_JITTER_MS;
    const waitMs = Math.round(wait + jitter);
    this.#log(`connection lost; redialing in ${waitMs}ms`);
    // The backoff is the host's decision, so the core never sees it. Emit
    // it as a host-range code rather than leaving it in a log line.
    this.#telemetry(HostEmit.redial, BigInt(waitMs), BigInt(this.#state.dialAttempts));
    this.#cancelRedial = this.#schedule(() => void this.#connect(), wait + jitter);
  }

  /**
   * Tears the connection down and tells the core. `session_disconnected`
   * is not optional: it drops the half-frame sitting in the reassembly
   * buffer, which would otherwise be read as the head of the next
   * connection's first frame, and it stops a queued resync from being
   * written into a stream that is already gone.
   */
  #closeConnection(): void {
    const conn = this.#conn;
    this.#conn = null;
    try {
      conn?.close();
    } catch {
      // already gone
    }
    const session = this.#session;
    if (session) this.#call(() => session.session_disconnected());
  }

  #onStreamBytes(bytes: Uint8Array): void {
    const session = this.#session;
    if (!session) return;
    const cap = session.session_cap();
    const now = BigInt(this.#now());
    for (let off = 0; off < bytes.length; off += cap) {
      const n = Math.min(cap, bytes.length - off);
      // The buffer is re-derived on every iteration: a call into the core
      // can grow its memory and detach any view taken before it.
      new Uint8Array(session.memory.buffer).set(
        bytes.subarray(off, off + n),
        session.session_buf(),
      );
      this.#call(() => session.session_on_stream(n, now));
      // Once framing is lost, the rest of this read is unparseable too;
      // feeding it in would be handing garbage to a core that already
      // said so.
      if (this.#pendingTeardown !== null) break;
    }
    this.#drainTeardown();
  }

  /**
   * Runs a teardown the core raised from inside a call we were making.
   * Closing the transport takes the ordinary death path — the connection
   * closes, the core is told, and the backoff redials — because a stream
   * whose framing is gone is not repairable in place.
   */
  #drainTeardown(): void {
    const reason = this.#pendingTeardown;
    if (reason === null) return;
    this.#pendingTeardown = null;
    this.#log(`stream framing lost (reason ${reason}); tearing down`);
    this.#telemetry(HostEmit.teardown, BigInt(reason), 0n);
    // Orphan the dead connection's callbacks first: its own `closed` may
    // still be in flight, and without this it would schedule a second
    // redial on top of the one below.
    this.#dialEpoch++;
    this.#onDead();
  }

  #onDatagram(bytes: Uint8Array): void {
    const session = this.#session;
    if (!session) return;
    if (bytes.length > session.session_cap()) {
      this.#log(`dropped a ${bytes.length}B datagram: over the session buffer`);
      return;
    }
    new Uint8Array(session.memory.buffer).set(bytes, session.session_buf());
    this.#call(() => session.session_on_datagram(bytes.length, BigInt(this.#now())));
  }

  // -------------------------------------------------------------------------
  // Host imports
  // -------------------------------------------------------------------------

  #hostImports(): HostImports {
    return {
      park_apply: (ptr, len) => {
        const replica = this.#replica;
        if (!replica || len > replica.io_cap()) return 1;
        this.#intoReplica(replica, ptr, len);
        return replica.sim_apply(len);
      },
      park_step: () => {
        this.#replica?.sim_step();
      },
      park_snapshot: (dst, cap) => {
        const replica = this.#replica;
        const session = this.#session;
        if (!replica || !session) return 0;
        const len = replica.sim_snapshot();
        if (len === 0 || len > cap) return 0;
        const at = replica.io_buf();
        new Uint8Array(session.memory.buffer).set(
          new Uint8Array(replica.memory.buffer, at, len),
          dst,
        );
        return len;
      },
      park_restore: (ptr, len) => {
        const replica = this.#replica;
        if (!replica || len > replica.io_cap()) return 1;
        this.#intoReplica(replica, ptr, len);
        return replica.sim_restore(len);
      },
      park_hash: () => this.#replica?.sim_hash() ?? 0n,
      park_tick: () => this.#replica?.sim_tick() ?? 0n,
      send_stream: (ptr, len) => {
        this.#conn?.sendStream(this.#fromSession(ptr, len));
      },
      send_datagram: (ptr, len) => {
        this.#conn?.sendDatagram(this.#fromSession(ptr, len));
      },
      inflate: (src, slen, dst, cap) => {
        const session = this.#session;
        if (!session) return 0;
        try {
          const out = inflateSync(this.#fromSession(src, slen));
          if (out.length === 0 || out.length > cap) return 0;
          new Uint8Array(session.memory.buffer).set(out, dst);
          return out.length;
        } catch {
          return 0;
        }
      },
      request: (kind, a) => {
        switch (kind) {
          case Request.needTerrain:
            this.#loadBlob(kind, a);
            return;
          case Request.needModule:
            void this.#swapModule(Number(BigInt.asUintN(32, a)));
            return;
          case Request.resyncWanted:
            this.#log(`resync requested (reason ${a})`);
            return;
          case Request.teardown:
            // Deferred, not done here: this runs INSIDE a session call, and
            // tearing down re-enters the module through
            // `session_disconnected`. The core hands out one `&mut Session`
            // from a static, so a nested call would alias it.
            this.#pendingTeardown = Number(BigInt.asUintN(32, a));
            return;
          default:
            this.#log(`unknown host request ${kind} (${a})`);
        }
      },
      emit: (kind, a, b) => {
        this.#telemetry(kind, a, b);
        this.#onEmit(kind, a, b);
      },
    };
  }

  #intoReplica(replica: ReplicaExports, ptr: number, len: number): void {
    const session = this.#session;
    if (!session) return;
    new Uint8Array(replica.memory.buffer).set(
      new Uint8Array(session.memory.buffer, ptr, len),
      replica.io_buf(),
    );
  }

  /** Copies out of the session module's memory. Callers may hold the result past a wasm call. */
  #fromSession(ptr: number, len: number): Uint8Array {
    const session = this.#session;
    if (!session) return new Uint8Array(0);
    return new Uint8Array(session.memory.buffer.slice(ptr, ptr + len));
  }

  #onEmit(kind: number, a: bigint, b: bigint): void {
    switch (kind) {
      case Emit.welcome: {
        const w = decodeWelcomeEmit(b);
        this.#patch({ epoch: Number(a), rateHz: w.hz, role: w.role });
        this.#log(`welcome: ${w.role}, epoch ${a}, ${w.hz}Hz`);
        return;
      }
      case Emit.snapshotRestored:
        this.#log(`state at seq ${a}, tick ${b}`);
        return;
      case Emit.resyncRequested:
        this.#log(`resync (reason ${a}) at seq ${b}`);
        return;
      case Emit.moduleSwapped:
        this.#patch({ replicaModuleWord: moduleWordHex(Number(BigInt.asUintN(32, a))) });
        return;
      default:
        return;
    }
  }

  // -------------------------------------------------------------------------
  // Platform errands
  // -------------------------------------------------------------------------

  /**
   * Fetches a blob artifact and loads it into the world before telling
   * the core it is ready. A snapshot restores into it, so a half-loaded
   * world would surface as an unexplained restore refusal (replica code
   * 4) if it went missing.
   */
  #loadBlob(kind: number, id: bigint): void {
    this.#blobQueue = this.#blobQueue.then(() => this.#runBlobLoad(kind, id));
  }

  async #runBlobLoad(kind: number, id: bigint): Promise<void> {
    const session = this.#session;
    if (this.#blobId === id && this.#blob) {
      // Already loaded into the live world: a second request for the same
      // one is a re-ask after a restore, not a reason to refetch.
      if (session) this.#call(() => session.session_terrain_ready(id, 1));
      return;
    }
    const hex = hex64(id);
    let ok = false;
    try {
      const blob = new Uint8Array(await this.#ports.fetchBlob(kind, hex));
      const replica = this.#replica;
      if (replica) loadBlobInto(replica, blob, hex);
      this.#blob = blob;
      this.#blobId = id;
      ok = true;
      this.#log(`blob ${hex.slice(0, 8)}: ${blob.length}B`);
    } catch (e) {
      this.#log(`blob ${hex.slice(0, 8)} failed: ${messageOf(e)}`);
    }
    if (session) this.#call(() => session.session_terrain_ready(id, ok ? 1 : 0));
  }

  /**
   * Replaces the world with an instance of the current replica module.
   * The core resyncs afterwards and restores a snapshot into it, so the
   * fresh instance needs only its blob back — no state carries over from
   * the retired module, by design.
   */
  async #swapModule(pw: number): Promise<void> {
    if (this.#fetchingModule) return;
    this.#fetchingModule = true;
    try {
      const bytes = await this.#ports.fetchModule("replica");
      const word = await this.#wordOf(bytes);
      if (word !== pw) {
        // The server only ever serves current bytes, so a disagreement
        // means we raced a further flip; adopt what we got and let the
        // next epoch event or verdict correct us.
        this.#log(
          `replica module fetch is ${moduleWordHex(word)}, not the announced ${moduleWordHex(pw)}`,
        );
      }
      // Everything is prepared on a local and published only once it has
      // all succeeded. A new module can legitimately refuse the blob we
      // hold — tightening its schema is exactly the kind of change that
      // ships with a module — and publishing first would leave a live
      // instance with no world, `session_module_swapped` never called, and
      // the core's latch stuck: a frozen world that every retry then fails
      // to repair in the same way.
      const staged = await instantiateReplica(bytes);
      const blob = this.#blob;
      if (blob) loadBlobInto(staged, blob, hex64(this.#blobId));
      this.#replica = staged;
      this.#fetchingModule = false;
      this.#log(`replica module ${moduleWordHex(word)} staged`);
      const session = this.#session;
      if (session) this.#call(() => session.session_module_swapped(word));
    } catch (e) {
      // The live instance is untouched, so the world keeps running on the
      // retired module until a retry succeeds.
      this.#log(`module swap failed: ${messageOf(e)}`);
      this.#cancelModuleRetry?.();
      this.#cancelModuleRetry = this.#schedule(() => {
        this.#cancelModuleRetry = null;
        this.#fetchingModule = false;
        void this.#swapModule(pw);
      }, MODULE_RETRY_MS);
    }
  }

  async #wordOf(bytes: ArrayBuffer): Promise<number> {
    const digest = await this.#sha256(bytes);
    return new DataView(digest).getUint32(0, true);
  }

  // -------------------------------------------------------------------------
  // Identity
  // -------------------------------------------------------------------------

  /**
   * Adopts a new identity mid-session — the sign-in upgrade, where an
   * anonymous spectator becomes a player without losing the world it has
   * been watching. Replica state and `since_seq` survive, so this is a
   * reconnect rather than a reload; pending intents are cleared, because
   * they were minted under the old identity and the fresh nonce means
   * their ids will never be acknowledged.
   */
  reidentify(actorId: bigint, role: RoleName): void {
    const session = this.#session;
    if (!session) throw new Error("reidentify before boot");
    this.#actorId = actorId;
    this.#call(() =>
      session.session_reidentify(
        actorId,
        Role[role],
        this.#ports.random32() >>> 0,
        BigInt(this.#now()),
      ),
    );
    // Optimistic until the next welcome says what the server granted.
    this.#patch({ role });
    this.#log(`reidentifying as ${role}; redialing`);
    // Bumping the epoch orphans the in-flight dial, and cancelling the
    // backoff timer stops a queued redial from racing the one below —
    // signing in while reconnecting is exactly when both are pending.
    this.#dialEpoch++;
    this.#cancelRedial?.();
    this.#cancelRedial = null;
    this.#closeConnection();
    this.#backoffMs = BACKOFF_MIN_MS;
    void this.#connect();
  }

  /** Hidden pages go quiet: the core stops sending check datagrams. */
  setVisible(visible: boolean): void {
    const session = this.#session;
    if (session) this.#call(() => session.session_set_visible(visible ? 1 : 0));
  }

  // -------------------------------------------------------------------------
  // Read surface
  // -------------------------------------------------------------------------

  /**
   * Advances the session by one frame. Drive it from the render loop; the
   * clock inside decides how many sim steps that turns into, within
   * `budgetUs` of frame CPU.
   *
   * A budget of 0 is "observe, do not step": the clock still samples and
   * escalates, events still apply, a resync still goes out — the replica
   * simply stops advancing. That is what a debug freeze wants, and it is
   * not the same as skipping the call, which stops the session dead and
   * leaves the very readouts a freeze exists to watch frozen too. A late
   * event can still drive rollback replay under a zero budget; what a
   * zero budget suppresses is clock-directed stepping.
   */
  pump(nowMs?: number, budgetUs?: number): PumpStatus {
    const session = this.#session;
    if (!session) return decodePumpStatus(0);
    const word = this.#call(() =>
      session.session_pump(BigInt(nowMs ?? this.#now()), budgetUs ?? this.#stepBudgetUs),
    );
    const status = decodePumpStatus(word);
    if (this.#state.phase === "live" || this.#state.phase === "resyncing") {
      this.#patch({ phase: status.resyncing ? "resyncing" : "live" });
    }
    this.#drainTeardown();
    return status;
  }

  get state(): HostState {
    return this.#state;
  }

  /**
   * Watches the whole state. Fires immediately with the current value,
   * then after any change. Returns the unsubscribe.
   */
  subscribe(fn: (s: HostState) => void): () => void {
    this.#subscribers.add(fn);
    fn(this.#state);
    return () => {
      this.#subscribers.delete(fn);
    };
  }

  /**
   * The session's diagnostics record, read live at the instant of the
   * call: trail vs its target, clock, counters. One raw dump the module
   * writes and this host decodes — a pane polls it a few times a second
   * and the bytes are never retained. Null before a session module is
   * live.
   */
  diag(): Diag | null {
    const session = this.#session;
    if (!session) return null;
    const len = this.#call(() => session.session_diag(BigInt(this.#now())));
    if (len < DIAG_BYTES) return null;
    this.#diagScratch.set(new Uint8Array(session.memory.buffer, session.session_buf(), DIAG_BYTES));
    return decodeDiag(this.#diagScratch);
  }

  /**
   * The session module's extension exports — the game verbs the framework
   * does not know — as a typed door. A name the module does not export
   * throws with that name when called, not a TypeError mid-frame; every
   * call runs under the re-entrancy guard.
   */
  extension<T>(): T {
    return this.#extensionDoor as T;
  }

  #extensionCall(name: string): (...args: unknown[]) => unknown {
    let door = this.#extensionCache.get(name);
    if (!door) {
      door = (...args: unknown[]) => {
        const session = this.#session;
        if (!session) throw new Error(`extension ${name} before boot`);
        const fn = (session as unknown as DynamicExports)[name];
        if (typeof fn !== "function") {
          throw new Error(`session module has no extension export ${name}`);
        }
        const out = this.#call(() => (fn as (...a: unknown[]) => unknown)(...args));
        this.#drainTeardown();
        return out;
      };
      this.#extensionCache.set(name, door);
    }
    return door;
  }

  /**
   * Calls a replica-module projection export and returns its `io_buf`
   * write as a scratch view — valid only until the next host call, so
   * decode or copy immediately.
   */
  readProjection(fn: string, ...args: (number | bigint)[]): Uint8Array {
    const replica = this.#replica;
    if (!replica) throw new Error(`projection ${fn} before boot`);
    const door = (replica as unknown as DynamicExports)[fn];
    if (typeof door !== "function") {
      throw new Error(`replica module has no projection export ${fn}`);
    }
    return this.#call(() => {
      const len = (door as (...a: (number | bigint)[]) => number)(...args);
      return new Uint8Array(replica.memory.buffer, replica.io_buf(), len);
    });
  }

  smoothFrame(nowMs: number): FrameQuads {
    const session = this.#session;
    if (!session) return { nowMs, phaseQ16: 0, smooth: () => {} };
    const phaseQ16 = this.#call(() => session.session_phase_q16());
    return {
      nowMs,
      phaseQ16,
      smooth: (quads, snapQ16) => {
        const cap = session.frame_cap();
        const total = Math.floor(quads.length / 4);
        this.#call(() => {
          for (let done = 0; done < total; done += cap) {
            const n = Math.min(cap, total - done);
            const buf = new Int32Array(session.memory.buffer, session.frame_buf(), n * 4);
            buf.set(quads.subarray(done * 4, (done + n) * 4));
            session.smooth_frame(n, phaseQ16, snapQ16);
            quads.set(new Int32Array(session.memory.buffer, session.frame_buf(), n * 4), done * 4);
          }
        });
      },
    };
  }

  #call<T>(fn: () => T): T {
    if (this.#inCall) {
      throw new Error("re-entrant wasm call refused: the session hands out one &mut Session");
    }
    this.#inCall = true;
    try {
      return fn();
    } finally {
      this.#inCall = false;
    }
  }

  #patch(next: Partial<HostState>): void {
    let changed = false;
    for (const key of Object.keys(next) as (keyof HostState)[]) {
      if (this.#state[key] !== next[key]) changed = true;
    }
    if (!changed) return;
    this.#state = { ...this.#state, ...next };
    for (const fn of this.#subscribers) fn(this.#state);
  }

  #log(line: string): void {
    this.#logLine?.(line);
  }
}

/** Verifies and instantiates replica bytes — the boot and the module-swap lane both come through here. */
async function instantiateReplica(bytes: ArrayBuffer): Promise<ReplicaExports> {
  const module = await WebAssembly.compile(bytes);
  refuseAbiDrift("replica module", module, { exports: REPLICA_EXPORTS, imports: [] });
  const instance = await WebAssembly.instantiate(module);
  const replica = instance.exports as unknown as ReplicaExports;
  refuseAbiVersion("replica module", replica.abi_version());
  return replica;
}

function refuseAbiDrift(
  name: string,
  module: WebAssembly.Module,
  required: { readonly exports: readonly string[]; readonly imports: readonly string[] },
): void {
  const violations = abiViolations(module, required);
  if (violations.length > 0) {
    throw new Error(`${name} does not fit this host: ${violations.join(", ")}`);
  }
}

function refuseAbiVersion(name: string, got: number): void {
  if (got !== ABI_VERSION) {
    throw new Error(`${name} speaks ABI v${got}; this host expects v${ABI_VERSION}`);
  }
}

function loadBlobInto(replica: ReplicaExports, blob: Uint8Array, hex: string): void {
  if (blob.length > replica.terrain_cap()) {
    throw new Error(`blob ${hex} is ${blob.length}B, over the module cap`);
  }
  new Uint8Array(replica.memory.buffer).set(blob, replica.terrain_buf());
  const code = replica.sim_set_terrain(blob.length);
  if (code !== 0) throw new Error(`blob ${hex} rejected (code ${code})`);
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

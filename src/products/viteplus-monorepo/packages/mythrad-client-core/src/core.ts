// The host. It owns two wasm instances — the park carrying the world and
// the client module carrying the session core — and does exactly four things:
// copies bytes between their linear memories on the park verbs, routes
// frames to and from the transport, performs the platform errands the
// core asks for (fetch terrain, fetch a module, inflate), and projects
// what the core knows into a read surface a renderer and a HUD can use.
//
// All protocol behavior — ordering, rollback, resync, strikes, intent
// identity, check cadence — lives in the session core. If a
// decision about the protocol is being made in this file, it is in the
// wrong place.

// fflate's `inflateSync` is the no-wrapper (raw DEFLATE) variant, which
// is what a snapshot frame carries.
import { inflateSync } from "fflate";
import {
  decodeDiag,
  decodeHud,
  clockStateOf,
  decodeWelcomeEmit,
  DIAG_BYTES,
  HUD_BYTES,
  PumpFlag,
  type ClientExports,
  type Diag,
  type HostImports,
  type Hud,
  type ParkExports,
} from "./abi.ts";
import { Emit, HostEmit, Request, type Connection, type Ports } from "./ports.ts";
import { decodeTerrain, type TerrainPlanes } from "./terrain.ts";
import { Role, moduleWordHex, hex64, type RoleName } from "./wire.ts";

const BACKOFF_MIN_MS = 300;
const BACKOFF_MAX_MS = 5000;
const BACKOFF_JITTER_MS = 250;
/** Frame CPU the clock may spend stepping per pump. */
export const DEFAULT_STEP_BUDGET_US = 8000;
/** How long a failed module fetch waits before trying again. */
const MODULE_RETRY_MS = 3000;
/** Straight dial failures before the surface should tell the user the park is unreachable. */
export const UNREACHABLE_AFTER_DIALS = 3;

/**
 * How long the presenter takes to absorb a re-derivation. The presented
 * world is the journal plus our unanswered intents, and re-deriving it is
 * how the two are kept from drifting apart — but a re-derivation moves
 * dogs without time passing, which is the one thing a renderer cannot
 * interpolate through. So the host keeps showing them where they were and
 * closes the gap over this window.
 */
export const GLIDE_MS = 150;

/**
 * The fastest the presenter will move a dog to close that gap, in cells
 * per second. A dog walks three, so this is a correction hurrying visibly
 * without becoming a teleport — and it is a RATE rather than a per-frame
 * step, so the bound holds at any refresh rate rather than only at the one
 * it was tuned on. A gap too large for `GLIDE_MS` at this speed takes
 * longer instead of moving faster.
 *
 * Twelve keeps the worst frame under a third of a cell on a 30Hz display,
 * which is the slowest refresh worth designing for; a faster display only
 * makes the same motion smoother.
 */
export const GLIDE_MAX_CELLS_PER_SEC = 12;

/**
 * The largest gap the presenter will close on the screen's behalf, in
 * cells. A re-derivation costs at most about a round trip of walking, so
 * anything past this is not a correction being hidden — it is a world
 * change, and gliding a dog across one pretends a continuity that did not
 * happen. Beyond the cap the dog simply arrives where the world says it
 * is, which is the honest picture of a world that really did change.
 */
export const GLIDE_MAX_CELLS = 4;

/** Q16.16, the fixed point the view and the sim share. */
const Q16 = 65536;

/** id u64, x i32, y i32, then the per-dog byte fields. */
const VIEW_RECORD_BYTES = 20;

/** Re-check for departed dogs about every four seconds of a 60Hz loop. */
const GLIDE_PRUNE_FRAMES = 256;

/** Smoothstep's peak slope: the fastest moment of a glide, relative to a linear one. */
const GLIDE_PEAK_SLOPE = 1.5;

/**
 * How long a correction of `cells` may take. Small ones take `GLIDE_MS`;
 * larger ones stretch, so the dog never crosses the gap faster than
 * `GLIDE_MAX_CELLS_PER_SEC` at the steepest point of the curve. Stretching
 * rather than capping means there is no cliff at any particular size — a
 * correction one cell bigger looks one cell bigger, not suddenly instant.
 */
function glideWindowMs(cells: number): number {
  const atFullSpeed = (GLIDE_PEAK_SLOPE * cells * 1000) / GLIDE_MAX_CELLS_PER_SEC;
  return Math.max(GLIDE_MS, atFullSpeed);
}

/**
 * How much of a glide is still owed after `elapsedMs` of its `windowMs`.
 * Smooth at both ends, so neither taking the glide on nor finishing it is
 * a kink in the dog's motion.
 */
function glideRemaining(elapsedMs: number, windowMs: number): number {
  if (elapsedMs <= 0) return 1;
  if (elapsedMs >= windowMs) return 0;
  const u = elapsedMs / windowMs;
  return 1 - u * u * (3 - 2 * u);
}

export type ConnectionStatus = "idle" | "connecting" | "connected" | "reconnecting";

/** Everything the host can tell a renderer or HUD about the session. */
export type ClientState = {
  readonly status: ConnectionStatus;
  /** Straight failed dials since the last success. */
  readonly dialFailures: number;
  /** The role the server granted, which may be narrower than the one we asked for. */
  readonly role: RoleName;
  /** The park this session asked for. Host-side: it is what `/session` was called with. */
  readonly park: string;
  readonly epoch: number;
  readonly hz: number;
  readonly tick: bigint;
  readonly seq: bigint;
  readonly present: boolean;
  readonly checkedInToday: boolean;
  readonly day: number;
  readonly dogCount: number;
  readonly parkEnergy: bigint;
  readonly selfEnergy: number;
  readonly boosting: boolean;
  readonly events: number;
  readonly rollbacks: number;
  readonly resyncs: number;
  readonly checks: number;
  readonly mismatches: number;
  readonly rejects: number;
  /** Stream plus datagram bytes since boot: the headline cost metric. */
  readonly bytesDown: number;
  /** 0 acquiring, 1 locked, 2 fast-forward, 3 snapshot-required. */
  readonly clockState: number;
  readonly rttMs: number;
  /** The last verdict's answer, or null before one arrives. */
  readonly worldOk: boolean | null;
  /** The running park module, as `/wt-info` spells it. */
  readonly parkWord: string;
};

/** What a renderer needs per frame. Valid until the next `view()` call. */
export type FrameView = {
  /** Slot 1's `sim_view` projection: u32 count, then 20-byte dog records. */
  readonly viewBytes: Uint8Array;
  readonly terrain: TerrainPlanes | null;
  /** Q16 phase between ticks, for interpolation. */
  readonly phaseQ16: number;
  readonly tick: bigint;
};

export type CoreOptions = {
  /** The dog this session speaks for. Spectators still pass one; the HUD reads it. */
  readonly myDog: bigint;
  readonly role: RoleName;
  /** The park name the app minted its session against, for display. */
  readonly park: string;
  /** Check cadence in milliseconds, from the netcode-check-seconds flag. */
  readonly checkMs: number;
  /** References from `/wt-info`, used to cache-bust the module fetches. */
  readonly moduleRefs?: { readonly park?: string; readonly client?: string };
  /**
   * Timer injection. A deterministic test drives the redial and retry
   * lanes through its own scheduler; the default is `setTimeout`.
   */
  readonly schedule?: (fn: () => void, ms: number) => () => void;
};

type Subscriber = { field: keyof ClientState; cb: (value: never) => void };

const defaultSchedule = (fn: () => void, ms: number): (() => void) => {
  const t = setTimeout(fn, ms);
  return () => clearTimeout(t);
};

export class Core {
  readonly #ports: Ports;
  readonly #options: CoreOptions;
  readonly #schedule: (fn: () => void, ms: number) => () => void;

  #client: ClientExports | null = null;
  /** The world. One instance: the journal is what the renderer reads. */
  #park: ParkExports | null = null;

  /** The dog the HUD reads. Follows `reidentify`, so it is not read from options. */
  #myDog: bigint;

  #terrain: TerrainPlanes | null = null;
  #terrainId = 0n;

  #conn: Connection | null = null;
  #dialEpoch = 0;
  #backoffMs = BACKOFF_MIN_MS;
  #cancelRedial: (() => void) | null = null;
  #disposed = false;

  // Terrain loads run one at a time and in request order: the core can
  // ask for world B while A is still in flight (a snapshot from a park
  // that re-terrained), and answering `ready` out of order would leave it
  // waiting on a load that already reported.
  #terrainQueue: Promise<void> = Promise.resolve();
  #fetchingModule = false;
  /** Cancels the module-swap retry, so a disposed Core stops instantiating modules. */
  #cancelModuleRetry: (() => void) | null = null;

  #boostHeld = false;
  #bytesDown = 0;
  /** A teardown raised from inside a session call, run once that call returns. */
  #pendingTeardown: number | null = null;
  #state: ClientState;
  readonly #subscribers = new Set<Subscriber>();

  /** Reused so a 60Hz render loop does not allocate a view copy per frame. */
  #viewScratch = new Uint8Array(0);
  readonly #hudScratch = new Uint8Array(HUD_BYTES);
  readonly #diagScratch = new Uint8Array(DIAG_BYTES);

  /** Where each dog was last shown, in Q16 view units, and when. */
  readonly #shown = new Map<bigint, { x: number; y: number; frame: number }>();
  /** What a re-derivation owes each dog, in Q16 view units, and since when. */
  readonly #glide = new Map<bigint, { dx: number; dy: number; sinceMs: number; ms: number }>();
  /** Raised by a pump that re-derived, consumed by the next `view()`. */
  #corrected = false;
  #viewFrame = 0;

  constructor(ports: Ports, options: CoreOptions) {
    this.#ports = ports;
    this.#options = options;
    this.#schedule = options.schedule ?? defaultSchedule;
    this.#myDog = options.myDog;
    this.#state = {
      status: "idle",
      dialFailures: 0,
      role: options.role,
      park: options.park,
      epoch: 0,
      hz: 0,
      tick: 0n,
      seq: 0n,
      present: false,
      checkedInToday: false,
      day: 0,
      dogCount: 0,
      parkEnergy: 0n,
      selfEnergy: 0,
      boosting: false,
      events: 0,
      rollbacks: 0,
      resyncs: 0,
      checks: 0,
      mismatches: 0,
      rejects: 0,
      bytesDown: 0,
      clockState: 0,
      rttMs: 0,
      worldOk: null,
      parkWord: "",
    };
  }

  // -------------------------------------------------------------------------
  // Boot
  // -------------------------------------------------------------------------

  /**
   * Instantiates the modules and dials. The caller has already resolved
   * identity and, if it needs one, the `/wt-info` references — this does
   * no auth and no service discovery.
   */
  async boot(): Promise<void> {
    const refs = this.#options.moduleRefs;
    const [parkBytes, clientBytes] = await Promise.all([
      this.#ports.fetchBehavior("park", refs?.park),
      this.#ports.fetchBehavior("client", refs?.client),
    ]);
    const imports = { host: this.#hostImports() } as unknown as WebAssembly.Imports;
    const client = (await WebAssembly.instantiate(clientBytes, imports)).instance
      .exports as unknown as ClientExports;
    this.#client = client;

    const parkWord = await this.#wordOf(parkBytes);
    this.#park = await instantiatePark(parkBytes);

    client.session_init(
      this.#myDog,
      Role[this.#options.role],
      this.#options.checkMs,
      this.#ports.random32() >>> 0,
      BigInt(this.#ports.now()),
    );
    client.session_set_visible(1);
    // Seeds the core's notion of which module the world is running, so the
    // first verdict compares against something. Safe here and nowhere
    // later: no world is live yet, so it has no state to disturb.
    client.session_module_swapped(parkWord);
    this.#patch({ parkWord: moduleWordHex(parkWord) });
    this.#log(`park module ${moduleWordHex(parkWord)} live`);
    void this.#connect();
  }

  /** Stops redialing and drops the current connection. The instances stay usable. */
  dispose(): void {
    this.#disposed = true;
    this.#dialEpoch++;
    this.#cancelRedial?.();
    this.#cancelRedial = null;
    // Without this a disposed Core still instantiates two wasm modules
    // three seconds later, on a session nothing is watching.
    this.#cancelModuleRetry?.();
    this.#cancelModuleRetry = null;
    this.#closeConnection();
  }

  // -------------------------------------------------------------------------
  // Connection lifecycle
  // -------------------------------------------------------------------------

  async #connect(): Promise<void> {
    if (this.#disposed) return;
    const client = this.#client;
    if (!client) throw new Error("connect before boot");
    const myEpoch = ++this.#dialEpoch;
    this.#patch({ status: this.#state.dialFailures > 0 ? "reconnecting" : "connecting" });
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
      this.#patch({ status: "connected", dialFailures: 0 });

      const ticket = dialed.ticket;
      if (ticket.length > client.session_cap()) {
        throw new Error(`ticket is ${ticket.length}B, over the session buffer`);
      }
      new Uint8Array(client.memory.buffer).set(ticket, client.session_buf());
      const code = client.session_connected(ticket.length, BigInt(this.#ports.now()));
      if (code !== 0) throw new Error(`hello refused (code ${code}); ticket may be too large`);
    } catch (e) {
      this.#log(`connect failed: ${messageOf(e)}`);
      this.#patch({ dialFailures: this.#state.dialFailures + 1 });
      if (myEpoch === this.#dialEpoch) this.#onDead();
    }
  }

  #onDead(): void {
    if (this.#disposed) return;
    this.#closeConnection();
    const wait = this.#backoffMs;
    this.#backoffMs = Math.min(this.#backoffMs * 2, BACKOFF_MAX_MS);
    this.#patch({ status: "reconnecting" });
    // Jitter comes from the injected randomness, so a deterministic run
    // reproduces the whole redial schedule.
    const jitter = ((this.#ports.random32() >>> 0) / 0x1_0000_0000) * BACKOFF_JITTER_MS;
    const waitMs = Math.round(wait + jitter);
    this.#log(`connection lost; redialing in ${waitMs}ms`);
    // The backoff is the host's decision, so the core never sees it. Emit
    // it as a host-range code rather than leaving it in a log line.
    this.#ports.telemetry(HostEmit.redial, BigInt(waitMs), BigInt(this.#state.dialFailures));
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
    this.#client?.session_disconnected();
  }

  #onStreamBytes(bytes: Uint8Array): void {
    const client = this.#client;
    if (!client) return;
    this.#bytesDown += bytes.length;
    const cap = client.session_cap();
    const now = BigInt(this.#ports.now());
    for (let off = 0; off < bytes.length; off += cap) {
      const n = Math.min(cap, bytes.length - off);
      // The buffer is re-derived on every iteration: a call into the core
      // can grow its memory and detach any view taken before it.
      new Uint8Array(client.memory.buffer).set(bytes.subarray(off, off + n), client.session_buf());
      client.session_on_stream(n, now);
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
    this.#ports.telemetry(HostEmit.teardown, BigInt(reason), 0n);
    // Orphan the dead connection's callbacks first: its own `closed` may
    // still be in flight, and without this it would schedule a second
    // redial on top of the one below.
    this.#dialEpoch++;
    this.#onDead();
  }

  #onDatagram(bytes: Uint8Array): void {
    const client = this.#client;
    if (!client) return;
    this.#bytesDown += bytes.length;
    if (bytes.length > client.session_cap()) {
      this.#log(`dropped a ${bytes.length}B datagram: over the session buffer`);
      return;
    }
    new Uint8Array(client.memory.buffer).set(bytes, client.session_buf());
    client.session_on_datagram(bytes.length, BigInt(this.#ports.now()));
  }

  // -------------------------------------------------------------------------
  // Host imports
  // -------------------------------------------------------------------------

  #hostImports(): HostImports {
    return {
      park_apply: (ptr, len) => {
        const park = this.#park;
        if (!park || len > park.io_cap()) return 1;
        this.#intoPark(park, ptr, len);
        return park.sim_apply(len);
      },
      park_step: () => {
        this.#park?.sim_step();
      },
      park_snapshot: (dst, cap) => {
        const park = this.#park;
        const client = this.#client;
        if (!park || !client) return 0;
        const len = park.sim_snapshot();
        if (len === 0 || len > cap) return 0;
        const at = park.io_buf();
        new Uint8Array(client.memory.buffer).set(new Uint8Array(park.memory.buffer, at, len), dst);
        return len;
      },
      park_restore: (ptr, len) => {
        const park = this.#park;
        if (!park || len > park.io_cap()) return 1;
        this.#intoPark(park, ptr, len);
        return park.sim_restore(len);
      },
      park_hash: () => this.#park?.sim_hash() ?? 0n,
      park_tick: () => this.#park?.sim_tick() ?? 0n,
      send_stream: (ptr, len) => {
        this.#conn?.sendStream(this.#fromClient(ptr, len));
      },
      send_datagram: (ptr, len) => {
        this.#conn?.sendDatagram(this.#fromClient(ptr, len));
      },
      inflate: (src, slen, dst, cap) => {
        const client = this.#client;
        if (!client) return 0;
        try {
          const out = inflateSync(this.#fromClient(src, slen));
          if (out.length === 0 || out.length > cap) return 0;
          new Uint8Array(client.memory.buffer).set(out, dst);
          return out.length;
        } catch {
          return 0;
        }
      },
      request: (kind, a) => {
        switch (kind) {
          case Request.needTerrain:
            this.#loadTerrain(a);
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
        this.#ports.telemetry(kind, a, b);
        this.#onEmit(kind, a, b);
      },
    };
  }

  #intoPark(park: ParkExports, ptr: number, len: number): void {
    const client = this.#client;
    if (!client) return;
    new Uint8Array(park.memory.buffer).set(
      new Uint8Array(client.memory.buffer, ptr, len),
      park.io_buf(),
    );
  }

  /** Copies out of the client's memory. Callers may hold the result past a wasm call. */
  #fromClient(ptr: number, len: number): Uint8Array {
    const client = this.#client;
    if (!client) return new Uint8Array(0);
    return new Uint8Array(client.memory.buffer.slice(ptr, ptr + len));
  }

  #onEmit(kind: number, a: bigint, b: bigint): void {
    switch (kind) {
      case Emit.welcome: {
        const w = decodeWelcomeEmit(b);
        const role = w.role === Role.player ? "player" : "spectator";
        const hz = w.hz;
        this.#patch({ epoch: Number(a), hz, role });
        this.#log(`welcome: ${role}, park ${this.#state.park}, epoch ${a}, ${hz}Hz`);
        return;
      }
      case Emit.verdict:
        this.#patch({ worldOk: a !== 0n, rttMs: Number(b) });
        return;
      case Emit.snapshotRestored:
        this.#log(`state at seq ${a}, tick ${b}`);
        return;
      case Emit.resyncRequested:
        this.#log(`resync (reason ${a}) at seq ${b}`);
        return;
      case Emit.mismatch:
        this.#patch({ worldOk: false });
        return;
      case Emit.moduleSwapped:
        this.#patch({ parkWord: moduleWordHex(Number(BigInt.asUintN(32, a))) });
        return;
      default:
        return;
    }
  }

  // -------------------------------------------------------------------------
  // Platform errands
  // -------------------------------------------------------------------------

  /**
   * Fetches a terrain artifact and loads it into the world before telling
   * the core it is ready. A snapshot restores into it, so a
   * half-loaded world would surface as an unexplained restore refusal
   * (park code 4) if it went missing.
   */
  #loadTerrain(id: bigint): void {
    this.#terrainQueue = this.#terrainQueue.then(() => this.#runTerrainLoad(id));
  }

  async #runTerrainLoad(id: bigint): Promise<void> {
    if (this.#terrainId === id && this.#terrain) {
      // Already loaded into the live world: a second request for the same
      // one is a re-ask after a restore, not a reason to refetch.
      this.#client?.session_terrain_ready(id, 1);
      return;
    }
    const hex = hex64(id);
    let ok = false;
    try {
      const blob = new Uint8Array(await this.#ports.fetchTerrain(hex));
      const planes = decodeTerrain(blob);
      if (this.#park) loadTerrainInto(this.#park, blob, hex);
      this.#terrain = planes;
      this.#terrainId = id;
      ok = true;
      this.#log(`terrain ${hex.slice(0, 8)}: ${planes.w}x${planes.h}`);
    } catch (e) {
      this.#log(`terrain ${hex.slice(0, 8)} failed: ${messageOf(e)}`);
    }
    this.#client?.session_terrain_ready(id, ok ? 1 : 0);
  }

  /**
   * Replaces the world with an instance of the current park module. The
   * core resyncs afterwards and restores a snapshot into it, so the fresh
   * instance needs only its terrain back — no state carries over from the
   * retired module, by design.
   */
  async #swapModule(pw: number): Promise<void> {
    if (this.#fetchingModule) return;
    this.#fetchingModule = true;
    try {
      const bytes = await this.#ports.fetchBehavior("park");
      const word = await this.#wordOf(bytes);
      if (word !== pw) {
        // The server only ever serves current bytes, so a disagreement
        // means we raced a further flip; adopt what we got and let the
        // next epoch event or verdict correct us.
        this.#log(
          `park module fetch is ${moduleWordHex(word)}, not the announced ${moduleWordHex(pw)}`,
        );
      }
      // Everything is prepared on a local and published only once it has
      // all succeeded. A new module can legitimately refuse the terrain we
      // hold — tightening the terrain schema is exactly the kind of change
      // that ships with a module — and publishing first would leave a live
      // instance with no world, `session_module_swapped` never called, and
      // the core's latch stuck: a frozen park that every retry then fails
      // to repair in the same way.
      const staged = await instantiatePark(bytes);
      const blob = this.#terrain?.blob;
      if (blob) loadTerrainInto(staged, blob, hex64(this.#terrainId));
      this.#park = staged;
      this.#fetchingModule = false;
      this.#log(`park module ${moduleWordHex(word)} staged`);
      this.#client?.session_module_swapped(word);
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
    const port = this.#ports;
    const digest = port.sha256
      ? await port.sha256(bytes)
      : await crypto.subtle.digest("SHA-256", bytes);
    return new DataView(digest).getUint32(0, true);
  }

  // -------------------------------------------------------------------------
  // Intents
  // -------------------------------------------------------------------------

  /**
   * Brings the dog into the park. A player session does NOT need this on
   * connect — the core sends its own join from `session_connected`, and a
   * second one just earns an "already present" reject. This is for the
   * explicit re-join a UI might offer.
   */
  join(): bigint {
    return this.#client?.intent_join(BigInt(this.#ports.now())) ?? 0n;
  }

  checkIn(): bigint {
    return this.#client?.intent_check_in(BigInt(this.#ports.now())) ?? 0n;
  }

  /** `node` is the terrain cell index, row-major: `y * w + x`. */
  moveTo(node: number): bigint {
    return this.#client?.intent_move_to(node, BigInt(this.#ports.now())) ?? 0n;
  }

  /**
   * Boost is a held button, and the sim journals only transitions, so a
   * repeat of the state we already sent is not worth a frame on the wire.
   */
  setBoost(on: boolean): bigint {
    if (this.#state.role !== "player" || this.#boostHeld === on) return 0n;
    this.#boostHeld = on;
    return this.#client?.intent_boost(on ? 1 : 0, BigInt(this.#ports.now())) ?? 0n;
  }

  /**
   * Adopts a new identity mid-session — the sign-in upgrade, where an
   * anonymous spectator becomes a player without losing the park it has
   * been watching. Replica state and `since_seq` survive, so this is a
   * reconnect rather than a reload; pending intents are cleared, because
   * they were minted under the old identity and the fresh nonce means
   * their ids will never be acknowledged.
   */
  reidentify(myDog: bigint, role: RoleName): void {
    const client = this.#client;
    if (!client) throw new Error("reidentify before boot");
    this.#myDog = myDog;
    // Boost is edge-triggered and its pending intent just went away, so
    // the held state we were tracking no longer describes any dog.
    this.#boostHeld = false;
    client.session_reidentify(
      myDog,
      Role[role],
      this.#ports.random32() >>> 0,
      BigInt(this.#ports.now()),
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
    this.#client?.session_set_visible(visible ? 1 : 0);
  }

  // -------------------------------------------------------------------------
  // Read surface
  // -------------------------------------------------------------------------

  /**
   * Advances the session by one frame and refreshes the read surface.
   * Drive it from the render loop; the clock inside decides how many sim
   * steps that turns into, within `budgetUs` of frame CPU.
   *
   * A budget of 0 is "observe, do not step": the clock still samples and
   * escalates, events still apply, a resync still goes out — the replica
   * simply stops advancing. That is what a debug freeze wants, and it is
   * not the same as skipping the call, which stops the session dead and
   * leaves the very readouts a freeze exists to watch frozen too. A late
   * event can still drive rollback replay under a zero budget; what a
   * zero budget suppresses is clock-directed stepping.
   */
  pump(budgetUs: number = DEFAULT_STEP_BUDGET_US): number {
    const client = this.#client;
    if (!client) return 0;
    const flags = client.session_pump(BigInt(this.#ports.now()), budgetUs);
    // Latched rather than read straight from the flags: several pumps can
    // pass between two frames, and the glide is owed from the last
    // position a viewer actually saw, not from the last pump.
    if ((flags & PumpFlag.corrected) !== 0) this.#corrected = true;
    this.#refresh(flags);
    this.#drainTeardown();
    return flags;
  }

  get state(): ClientState {
    return this.#state;
  }

  // ---- diagnostics ----

  /**
   * The session's diagnostics record, read live at the instant of the
   * call: trail vs its target, clock, counters. One raw dump the module
   * writes and this host decodes — a pane polls it a few times a second,
   * `ClientState` is refreshed from the same record once per pump, and
   * the bytes are never retained. Null before a session module is live.
   */
  diag(): Diag | null {
    const client = this.#client;
    if (!client) return null;
    const len = client.session_diag(BigInt(this.#ports.now()));
    if (len < DIAG_BYTES) return null;
    this.#diagScratch.set(new Uint8Array(client.memory.buffer, client.session_buf(), DIAG_BYTES));
    return decodeDiag(this.#diagScratch);
  }

  /**
   * The canonical hash of the world, as the authority would compute it.
   * This is what a check datagram carries, so it is the one number that
   * says whether this replica and the park agree.
   */
  worldHash(): bigint {
    return this.#park?.sim_hash() ?? 0n;
  }

  /**
   * How much correction the presenter is still carrying, in cells, summed
   * over every dog. Zero means the view is showing the world exactly as
   * the session holds it.
   *
   * This exists for the same reason `worldHash` does: it is the only way to
   * state the invariant that the glide is TEMPORARY. A presenter that
   * never pays its debt off shows a wrong position forever, and every
   * continuity gate in the suite would pass while it did.
   */
  glideDebtCells(): number {
    const now = this.#ports.now();
    let owed = 0;
    for (const g of this.#glide.values()) {
      const left = glideRemaining(now - g.sinceMs, g.ms);
      owed += (Math.abs(g.dx) + Math.abs(g.dy)) * left;
    }
    return owed / Q16;
  }

  /**
   * Watches one field. Fires immediately with the current value, then
   * after any pump that changes it. Returns the unsubscribe.
   */
  subscribe<K extends keyof ClientState>(
    field: K,
    cb: (value: ClientState[K]) => void,
  ): () => void {
    const entry = { field, cb } as unknown as Subscriber;
    this.#subscribers.add(entry);
    cb(this.#state[field]);
    return () => {
      this.#subscribers.delete(entry);
    };
  }

  /**
   * The renderer's frame. `viewBytes` aliases a buffer this instance
   * reuses, so a caller that needs it past the next `view()` must copy.
   *
   * The positions here are the presented ones, glide included. That is
   * deliberate and it is the whole point of doing it in this method: a
   * renderer, a probe ring, a jank counter and a test all read the same
   * numbers, so none of them can disagree about where a dog was, and none
   * of them needs to know that the world was corrected under them.
   */
  view(): FrameView | null {
    const park = this.#park;
    const client = this.#client;
    if (!park || !client) return null;
    const len = park.sim_view();
    if (this.#viewScratch.length < len) this.#viewScratch = new Uint8Array(len * 2);
    const out = this.#viewScratch.subarray(0, len);
    out.set(new Uint8Array(park.memory.buffer, park.io_buf(), len));
    this.#glideView(out);
    return {
      viewBytes: out,
      terrain: this.#terrain,
      phaseQ16: client.session_phase_q16(),
      tick: park.sim_tick(),
    };
  }

  /**
   * Carries the screen across a re-derivation. On the frame after one, each
   * dog is owed the difference between where it was last shown and where
   * the re-derived world puts it; that debt is paid off over `GLIDE_MS`,
   * so what a viewer sees is continuous even though the world underneath
   * moved without time passing.
   *
   * The world is not edited — only this copy is. The park instance keeps
   * whatever the session put in it, so hashes and checks are untouched by
   * anything here.
   */
  #glideView(out: Uint8Array): void {
    const dv = new DataView(out.buffer, out.byteOffset, out.byteLength);
    const n = dv.getUint32(0, true);
    const now = this.#ports.now();
    const frame = ++this.#viewFrame;

    if (this.#corrected) {
      this.#corrected = false;
      for (let i = 0; i < n; i++) {
        const at = 4 + i * VIEW_RECORD_BYTES;
        const was = this.#shown.get(dv.getBigUint64(at, true));
        // A dog nobody has seen yet has no continuity to keep.
        if (!was) continue;
        const dx = was.x - dv.getInt32(at + 8, true);
        const dy = was.y - dv.getInt32(at + 12, true);
        if (dx === 0 && dy === 0) continue;
        // Measured as a distance, not per axis: a correction that is three
        // cells north AND three cells east is a five-cell correction, and
        // capping the axes would let it through as two small ones.
        const cells = Math.hypot(dx, dy) / Q16;
        if (cells > GLIDE_MAX_CELLS) continue;
        const ms = glideWindowMs(cells);
        this.#glide.set(dv.getBigUint64(at, true), { dx, dy, sinceMs: now, ms });
      }
    }

    for (let i = 0; i < n; i++) {
      const at = 4 + i * VIEW_RECORD_BYTES;
      const id = dv.getBigUint64(at, true);
      let x = dv.getInt32(at + 8, true);
      let y = dv.getInt32(at + 12, true);
      const owed = this.#glide.get(id);
      if (owed) {
        const left = glideRemaining(now - owed.sinceMs, owed.ms);
        if (left <= 0) this.#glide.delete(id);
        else {
          x += Math.round(owed.dx * left);
          y += Math.round(owed.dy * left);
          dv.setInt32(at + 8, x, true);
          dv.setInt32(at + 12, y, true);
        }
      }
      const seen = this.#shown.get(id);
      if (seen) {
        seen.x = x;
        seen.y = y;
        seen.frame = frame;
      } else {
        this.#shown.set(id, { x, y, frame });
      }
    }

    // Dogs that have left the park stop being anyone's continuity.
    if (frame % GLIDE_PRUNE_FRAMES === 0) {
      for (const [id, seen] of this.#shown) {
        if (seen.frame === frame) continue;
        this.#shown.delete(id);
        this.#glide.delete(id);
      }
    }
  }

  /**
   * Runs the presentation smoother over `quads` in place: four Q16.16
   * ints per dog — previous x, previous y, current x, current y — with the
   * presented position written back into the first two. Batched against
   * the module's frame buffer, which holds `frame_cap()` dogs at a time.
   */
  smooth(quads: Int32Array, alphaQ16: number, snapQ16: number): void {
    const client = this.#client;
    if (!client) return;
    const cap = client.frame_cap();
    const total = Math.floor(quads.length / 4);
    for (let done = 0; done < total; done += cap) {
      const n = Math.min(cap, total - done);
      const buf = new Int32Array(client.memory.buffer, client.frame_buf(), n * 4);
      buf.set(quads.subarray(done * 4, (done + n) * 4));
      client.smooth_frame(n, alphaQ16, snapQ16);
      quads.set(new Int32Array(client.memory.buffer, client.frame_buf(), n * 4), done * 4);
    }
  }

  /**
   * `pumpFlags` carries the clock state, and it is the ONLY source for
   * it: the module's standalone `clock_state`/`clock_phase` exports read a
   * different Clock instance than the session core disciplines, so they
   * report a clock nothing has ever sampled.
   */
  #refresh(pumpFlags: number): void {
    const client = this.#client;
    const park = this.#park;
    if (!client) return;
    let hud: Hud | null = null;
    if (park) {
      const len = park.sim_hud(this.#myDog);
      if (len >= HUD_BYTES) {
        this.#hudScratch.set(new Uint8Array(park.memory.buffer, park.io_buf(), HUD_BYTES));
        hud = decodeHud(this.#hudScratch);
      }
    }
    const diag = this.diag();
    if (!diag) return;
    this.#patch({
      tick: diag.tick,
      seq: diag.seq,
      events: diag.events,
      rollbacks: diag.rollbacks,
      resyncs: diag.resyncs,
      checks: diag.checks,
      mismatches: diag.mismatches,
      rejects: diag.rejects,
      bytesDown: this.#bytesDown,
      clockState: clockStateOf(pumpFlags),
      rttMs: diag.rttMs,
      ...(hud
        ? {
            present: hud.present,
            checkedInToday: hud.checkedInToday,
            day: hud.day,
            dogCount: hud.dogCount,
            parkEnergy: hud.parkEnergy,
            selfEnergy: hud.selfEnergy,
            boosting: hud.boosting,
          }
        : {}),
    });
  }

  #patch(next: Partial<ClientState>): void {
    const changed: (keyof ClientState)[] = [];
    for (const key of Object.keys(next) as (keyof ClientState)[]) {
      if (this.#state[key] !== next[key]) changed.push(key);
    }
    if (changed.length === 0) return;
    this.#state = { ...this.#state, ...next };
    for (const sub of this.#subscribers) {
      if (changed.includes(sub.field)) (sub.cb as (v: unknown) => void)(this.#state[sub.field]);
    }
  }

  #log(line: string): void {
    this.#ports.log?.(line);
  }
}

async function instantiatePark(bytes: ArrayBuffer): Promise<ParkExports> {
  const { instance } = await WebAssembly.instantiate(bytes);
  return instance.exports as unknown as ParkExports;
}

function loadTerrainInto(park: ParkExports, blob: Uint8Array, hex: string): void {
  if (blob.length > park.terrain_cap()) {
    throw new Error(`terrain ${hex} is ${blob.length}B, over the module cap`);
  }
  new Uint8Array(park.memory.buffer).set(blob, park.terrain_buf());
  const code = park.sim_set_terrain(blob.length);
  if (code !== 0) throw new Error(`terrain ${hex} rejected (code ${code})`);
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

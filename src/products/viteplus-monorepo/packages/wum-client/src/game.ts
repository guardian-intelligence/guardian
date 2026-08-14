// Wake Up Mythra's client, as a thin game layer over the game-agnostic
// replica host: the intent verbs, the HUD and view projections, the
// terrain decode, and the glide presenter. Protocol behavior stays in the
// session core; byte routing stays in the host; what is here is only what
// makes the replica a dog park.

import {
  Emit,
  ReplicaHost,
  type HostOptions,
  type Ports,
  type PumpStatus,
} from "@guardian/chunkies";
import { type WumIntents } from "./intents.ts";
import { HUD_BYTES, decodeHud } from "./projections/hud.ts";
import { GlidePresenter, type FrameView } from "./projections/view.ts";
import { type GameState, type RoleName } from "./state.ts";
import { decodeTerrain, type TerrainPlanes } from "./terrain.ts";

/** The blob kind the session core's terrain request verb carries. */
const TERRAIN_BLOB_KIND = 1;

export type GameOptions = {
  /** The dog this session speaks for. Spectators still pass one; the HUD reads it. */
  readonly myDog: bigint;
  readonly role: RoleName;
  /** Check cadence in milliseconds, from the netcode-check-seconds flag. */
  readonly checkMs?: number;
  /** References from `/wt-info`, used to cache-bust the module fetches. */
  readonly moduleRefs?: { readonly session?: string; readonly replica?: string };
  readonly now?: () => number;
  readonly schedule?: (fn: () => void, ms: number) => () => void;
  readonly telemetry?: (code: number, a: bigint, b: bigint) => void;
  readonly sha256?: (bytes: ArrayBuffer) => Promise<ArrayBuffer>;
  readonly log?: (line: string) => void;
};

export class WumGame {
  readonly host: ReplicaHost;
  readonly #intents: WumIntents;
  readonly #now: () => number;
  readonly #glide = new GlidePresenter();
  readonly #subscribers = new Set<(s: GameState) => void>();

  #myDog: bigint;
  #terrain: TerrainPlanes | null = null;
  #boostHeld = false;
  /**
   * Latched rather than read straight from the pump: several pumps can
   * pass between two frames, and the glide is owed from the last position
   * a viewer actually saw, not from the last pump.
   */
  #corrected = false;
  #state: GameState;

  /** Reused so a 60Hz render loop does not allocate a view copy per frame. */
  #viewScratch = new Uint8Array(0);

  static create(ports: Ports, options: GameOptions): WumGame {
    return new WumGame(ports, options);
  }

  private constructor(ports: Ports, options: GameOptions) {
    this.#now = options.now ?? Date.now;
    this.#myDog = options.myDog;
    const gamePorts: Ports = {
      transport: ports.transport,
      fetchModule: (slot, ref) => ports.fetchModule(slot, ref),
      // The game reads the terrain off the fetch the host asked for, so
      // the renderer's planes and the sim's blob are always the same
      // bytes — and a blob this decode refuses is a failed load to the
      // host too, not a world the renderer silently cannot draw.
      fetchBlob: async (kind, id) => {
        const bytes = await ports.fetchBlob(kind, id);
        if (kind === TERRAIN_BLOB_KIND) this.#terrain = decodeTerrain(new Uint8Array(bytes));
        return bytes;
      },
      random32: () => ports.random32(),
    };
    const hostOptions: HostOptions = {
      actorId: options.myDog,
      role: options.role,
      telemetry: (code, a, b) => {
        options.telemetry?.(code, a, b);
        this.#onEmit(code, a);
      },
      ...(options.checkMs !== undefined ? { checkMs: options.checkMs } : {}),
      ...(options.moduleRefs !== undefined ? { moduleRefs: options.moduleRefs } : {}),
      ...(options.now !== undefined ? { now: options.now } : {}),
      ...(options.schedule !== undefined ? { schedule: options.schedule } : {}),
      ...(options.sha256 !== undefined ? { sha256: options.sha256 } : {}),
      ...(options.log !== undefined ? { log: options.log } : {}),
    };
    this.host = new ReplicaHost(gamePorts, hostOptions);
    this.#intents = this.host.extension<WumIntents>();
    this.#state = {
      connection: this.host.state,
      hud: null,
      worldTick: 0n,
      worldHashOk: null,
    };
    this.host.subscribe((connection) => this.#patch({ connection }));
  }

  boot(): Promise<void> {
    return this.host.boot();
  }

  dispose(): void {
    this.host.dispose();
  }

  get state(): GameState {
    return this.#state;
  }

  /** The dog this session speaks for; follows `reidentify`. */
  get myDog(): bigint {
    return this.#myDog;
  }

  /**
   * Watches the whole game state. Fires immediately with the current
   * value, then after any change. Returns the unsubscribe.
   */
  subscribe(fn: (s: GameState) => void): () => void {
    this.#subscribers.add(fn);
    fn(this.#state);
    return () => {
      this.#subscribers.delete(fn);
    };
  }

  pump(nowMs?: number, budgetUs?: number): PumpStatus {
    const status = this.host.pump(nowMs, budgetUs);
    if (status.corrected) this.#corrected = true;
    this.#refresh();
    return status;
  }

  /**
   * The renderer's frame. `viewBytes` aliases a buffer this instance
   * reuses, so a caller that needs it past the next `frame()` must copy.
   *
   * The positions here are the presented ones, glide included. That is
   * deliberate: a renderer, a probe ring, a jank counter and a test all
   * read the same numbers, so none of them can disagree about where a dog
   * was, and none of them needs to know that the world was corrected
   * under them.
   */
  frame(nowMs: number): FrameView | null {
    if (!this.#worldLive()) return null;
    const view = this.host.readProjection("sim_view");
    if (this.#viewScratch.length < view.length) {
      this.#viewScratch = new Uint8Array(view.length * 2);
    }
    const out = this.#viewScratch.subarray(0, view.length);
    out.set(view);
    const corrected = this.#corrected;
    this.#corrected = false;
    this.#glide.apply(out, nowMs, corrected);
    return {
      viewBytes: out,
      terrain: this.#terrain,
      phaseQ16: this.host.smoothFrame(nowMs).phaseQ16,
      tick: this.#state.worldTick,
    };
  }

  terrain(): TerrainPlanes | null {
    return this.#terrain;
  }

  /** See `GlidePresenter.debtCells`: correction the presenter still owes the screen. */
  glideDebtCells(): number {
    return this.#glide.debtCells(this.#now());
  }

  /**
   * Brings the dog into the park. A player session does NOT need this on
   * connect — the core sends its own join from `session_connected`, and a
   * second one just earns an "already present" reject. This is for the
   * explicit re-join a UI might offer.
   */
  join(): bigint {
    return this.#worldLive() ? this.#intents.intent_join(BigInt(this.#now())) : 0n;
  }

  checkIn(): bigint {
    return this.#worldLive() ? this.#intents.intent_check_in(BigInt(this.#now())) : 0n;
  }

  /** Sends the dog walking to the terrain cell at `(x, y)`. */
  moveTo(x: number, y: number): bigint {
    const t = this.#terrain;
    if (!this.#worldLive() || !t) return 0n;
    if (x < 0 || y < 0 || x >= t.w || y >= t.h) return 0n;
    return this.#intents.intent_move_to(y * t.w + x, BigInt(this.#now()));
  }

  /**
   * Boost is a held button, and the sim journals only transitions, so a
   * repeat of the state we already sent is not worth a frame on the wire.
   */
  setBoost(on: boolean): bigint {
    if (this.host.state.role !== "player" || this.#boostHeld === on) return 0n;
    if (!this.#worldLive()) return 0n;
    this.#boostHeld = on;
    return this.#intents.intent_boost(on ? 1 : 0, BigInt(this.#now()));
  }

  reidentify(myDog: bigint, role: RoleName): void {
    this.#myDog = myDog;
    // Boost is edge-triggered and its pending intent just went away, so
    // the held state we were tracking no longer describes any dog.
    this.#boostHeld = false;
    this.host.reidentify(myDog, role);
  }

  #worldLive(): boolean {
    return this.host.state.replicaModuleWord !== "";
  }

  #refresh(): void {
    if (!this.#worldLive()) return;
    const diag = this.host.diag();
    if (!diag) return;
    const hudBytes = this.host.readProjection("sim_hud", this.#myDog);
    const hud = hudBytes.length >= HUD_BYTES ? decodeHud(hudBytes) : null;
    this.#patch({
      worldTick: diag.tick,
      // A frame the module cannot answer keeps the last known HUD rather
      // than blanking it.
      ...(hud ? { hud: hudEquals(hud, this.#state.hud) ? this.#state.hud : hud } : {}),
    });
  }

  #onEmit(code: number, a: bigint): void {
    if (code === Emit.verdict) this.#patch({ worldHashOk: a !== 0n });
    else if (code === Emit.mismatch) this.#patch({ worldHashOk: false });
  }

  #patch(next: Partial<GameState>): void {
    let changed = false;
    for (const key of Object.keys(next) as (keyof GameState)[]) {
      if (this.#state[key] !== next[key]) changed = true;
    }
    if (!changed) return;
    this.#state = { ...this.#state, ...next };
    for (const fn of this.#subscribers) fn(this.#state);
  }
}

function hudEquals(a: NonNullable<GameState["hud"]>, b: GameState["hud"]): boolean {
  return (
    b !== null &&
    a.present === b.present &&
    a.checkedInToday === b.checkedInToday &&
    a.day === b.day &&
    a.dogCount === b.dogCount &&
    a.parkEnergy === b.parkEnergy &&
    a.selfEnergy === b.selfEnergy &&
    a.boosting === b.boosting
  );
}

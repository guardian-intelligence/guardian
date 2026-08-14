// The page chrome: every DOM write outside the canvas, driven by the
// game's read surface. The element ids and the exact text they carry are
// a test contract — the degradation drill reads #status, #rtt and #world,
// and headless drill harnesses read __mythraDiag — so the strings here
// are load-bearing, not decoration.

import type { HostState } from "@guardian/chunkies";
import type { GameState, WumGame } from "@guardian/wum-client";
import * as v from "valibot";

// Sim reject codes 1-9 and doorman codes 100-101 (rejectReasonName in
// mythrad session.go is the server-side mirror).
const REJECT_TEXT = {
  1: "the park couldn't read that intent",
  2: "your dog is already in the park",
  3: "your dog isn't in the park",
  4: "the park is full",
  5: "already checked in today",
  6: "the park doesn't know that action",
  7: "the park moved on to a new epoch",
  8: "your dog can't stand there",
  9: "the park's terrain is still loading",
  10: "already doing that",
  100: "spectators can't act — sign in to play",
  101: "that dog isn't yours",
} as const;
const RejectCode = v.picklist([1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 100, 101]);

/** The user-facing sentence for a reject reason code. */
export function rejectText(reason: number): string {
  const known = v.safeParse(RejectCode, reason);
  return known.success ? REJECT_TEXT[known.output] : `the park refused that (reason ${reason})`;
}

const UNREACHABLE =
  "Can't reach the park from this browser right now — the pack plays on without us. Still retrying.";

/** Read by headless drill harnesses; cell coordinates are Q16.16. */
export type Diag = {
  bytesDown: number;
  events: number;
  checks: number;
  mismatches: number;
  resyncs: number;
  rollbacks: number;
  rejects: number;
  tick: number;
  startedAt: number;
  myX: number;
  myY: number;
  camX: number;
  camY: number;
  camFree: boolean;
};

/** The buttons, for whoever owns what they do. The HUD owns how they look. */
export type Controls = {
  readonly signin: HTMLElement;
  readonly checkin: HTMLButtonElement;
  readonly boost: HTMLElement;
  readonly recenter: HTMLElement;
};

export type Hud = {
  /** Mutable, and published as `__mythraDiag`: the renderer writes into it too. */
  readonly diag: Diag;
  readonly controls: Controls;
  readonly log: (line: string) => void;
  /** A status the game never reaches: an unsupported browser, a failed boot. */
  readonly setStatus: (text: string) => void;
  readonly setWho: (text: string) => void;
  /** Anonymous spectators get the way in; signed-in players don't. */
  readonly setAnon: (anon: boolean) => void;
  readonly showRecenter: (show: boolean) => void;
  /** The roster line, from the renderer's read of the presented world. */
  readonly setRoster: (names: string[], total: number) => void;
  /** Cumulative downlink bytes, from the transport's own count. */
  readonly noteBytes: (total: number) => void;
  /** Starts the subscription that drives every field the game knows. */
  readonly bind: (game: WumGame, park: string) => void;
  /**
   * Refreshes the diagnostics-driven readouts (rtt, repair counters).
   * Called from the frame loop, never from a subscription: a state
   * notification can fire from inside a wasm call, where reading the
   * diagnostics record would re-enter the session module.
   */
  readonly update: () => void;
};

/** The pill text for a phase — `connected` is the drill contract for a live session. */
function statusText(connection: HostState): string {
  switch (connection.phase) {
    case "live":
    case "resyncing":
      return "connected";
    case "booting":
      return "connecting";
    case "connecting":
    case "unreachable":
      return connection.dialAttempts > 0 ? "reconnecting" : "connecting";
    default:
      return connection.phase;
  }
}

function el(id: string): HTMLElement {
  return v.parse(v.instance(HTMLElement), document.getElementById(id));
}

function button(id: string): HTMLButtonElement {
  return v.parse(v.instance(HTMLButtonElement), document.getElementById(id));
}

export function createHud(): Hud {
  const nodes = {
    status: el("status"),
    tick: el("tick"),
    rtt: el("rtt"),
    world: el("world"),
    bytes: el("bytes"),
    energy: el("energy"),
    role: el("role"),
    signin: el("signin"),
    checkin: button("checkin"),
    boost: el("boost"),
    recenter: el("recenter"),
    who: el("who"),
    log: el("log"),
  };

  const diag: Diag = {
    bytesDown: 0,
    events: 0,
    checks: 0,
    mismatches: 0,
    resyncs: 0,
    rollbacks: 0,
    rejects: 0,
    tick: 0,
    startedAt: 0,
    myX: 0,
    myY: 0,
    camX: 0,
    camY: 0,
    camFree: false,
  };
  Object.assign(globalThis, { __mythraDiag: diag });

  let connected = false;
  let whoText = "";
  let boundGame: WumGame | null = null;

  const log = (line: string): void => {
    const row = document.createElement("div");
    row.innerHTML = `<span class="t">${new Date().toTimeString().slice(0, 8)}</span> ${line}`;
    nodes.log.prepend(row);
    while (nodes.log.children.length > 80) nodes.log.lastChild?.remove();
  };

  const setStatus = (text: string): void => {
    nodes.status.textContent = text.toUpperCase();
    nodes.status.className = "pill" + (text === "connected" ? " connected" : "");
  };

  const drawBytes = (): void => {
    // The headline cost metric: downlink bytes per hour of screen-on
    // time, once there is enough screen-on time to divide by.
    const hours = (Date.now() - (diag.startedAt || Date.now())) / 3600000;
    nodes.bytes.textContent =
      hours > 0.001 ? `${(diag.bytesDown / 1024 / hours).toFixed(1)}KB/h` : `${diag.bytesDown}B`;
  };

  return {
    diag,
    controls: {
      signin: nodes.signin,
      checkin: nodes.checkin,
      boost: nodes.boost,
      recenter: nodes.recenter,
    },
    log,
    setStatus,
    setWho: (text) => {
      whoText = text;
      nodes.who.textContent = text;
    },
    setAnon: (anon) => {
      nodes.signin.style.display = anon ? "" : "none";
    },
    showRecenter: (show) => {
      nodes.recenter.style.display = show ? "" : "none";
    },
    setRoster: (names, total) => {
      // The roster line only speaks for the park while we're attached to
      // it — a replica that never connected has zero dogs and would
      // otherwise report "the park is empty" to a user we couldn't reach.
      if (!connected) return;
      const text =
        total === 0
          ? "the park is empty"
          : `dogs here: ${names.join(", ")}${total > names.length ? ` +${total - names.length} more` : ""}`;
      // Called per frame; only touch the DOM when the line changes.
      if (text === whoText) return;
      whoText = text;
      nodes.who.textContent = text;
    },
    noteBytes: (total) => {
      diag.bytesDown = total;
      drawBytes();
    },
    bind: (game, park) => {
      let shownStatus = "";
      let shownUnreachable = false;
      let prev: GameState | null = null;

      const checkinState = (s: GameState): void => {
        const player = s.connection.role === "player";
        nodes.checkin.style.display = player ? "" : "none";
        nodes.boost.style.display = player ? "" : "none";
        const checkedIn = s.hud?.checkedInToday ?? false;
        nodes.checkin.disabled = !player || !(s.hud?.present ?? false) || checkedIn;
        nodes.checkin.textContent = checkedIn ? "Checked in ✓" : "Check in";
      };

      game.subscribe((s) => {
        const status = statusText(s.connection);
        connected = s.connection.phase === "live" || s.connection.phase === "resyncing";
        if (connected && diag.startedAt === 0) diag.startedAt = Date.now();
        if (status !== shownStatus) {
          shownStatus = status;
          setStatus(status);
        }
        // Dials keep retrying, but the user should know the park is
        // unreachable rather than empty (iOS WebKit's WebTransport
        // networking crash looks exactly like this).
        if (s.connection.phase === "unreachable" && !shownUnreachable) {
          nodes.who.textContent = UNREACHABLE;
        }
        shownUnreachable = s.connection.phase === "unreachable";

        if (s.connection.role !== prev?.connection.role) {
          nodes.role.textContent = `${s.connection.role ?? "spectator"} @ ${park}`;
        }
        if (
          s.connection.role !== prev?.connection.role ||
          s.hud?.present !== prev?.hud?.present ||
          s.hud?.checkedInToday !== prev?.hud?.checkedInToday
        ) {
          checkinState(s);
        }
        if (s.worldTick !== prev?.worldTick) {
          diag.tick = Number(s.worldTick);
          nodes.tick.textContent = String(s.worldTick);
        }
        if (s.worldHashOk !== prev?.worldHashOk) {
          nodes.world.textContent = s.worldHashOk === null ? "–" : s.worldHashOk ? "✓" : "✗";
        }
        if (s.hud?.parkEnergy !== prev?.hud?.parkEnergy) {
          nodes.energy.textContent = String(s.hud?.parkEnergy ?? 0n);
        }
        prev = s;
      });
      boundGame = game;
    },
    update: () => {
      const d = boundGame?.host.diag();
      if (!d) return;
      diag.events = d.events;
      diag.rollbacks = d.rollbacks;
      diag.resyncs = d.resyncs;
      diag.checks = d.checks;
      diag.mismatches = d.mismatches;
      diag.rejects = d.rejects;
      const rtt = `${d.rttMs.toFixed(0)}ms`;
      if (nodes.rtt.textContent !== rtt) nodes.rtt.textContent = rtt;
    },
  };
}

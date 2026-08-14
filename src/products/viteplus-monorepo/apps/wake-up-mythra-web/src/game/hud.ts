// The page chrome: every DOM write outside the canvas, driven by the
// session core's read surface. The element ids and the exact text they
// carry are a test contract — the degradation drill reads #status, #rtt
// and #world, and headless drill harnesses read __mythraDiag — so the
// strings here are load-bearing, not decoration.

import { UNREACHABLE_AFTER_DIALS, type Core } from "@guardian/mythrad-client-core";
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
  /** A status the core never reaches: an unsupported browser, a failed boot. */
  readonly setStatus: (text: string) => void;
  readonly setWho: (text: string) => void;
  /** Anonymous spectators get the way in; signed-in players don't. */
  readonly setAnon: (anon: boolean) => void;
  readonly showRecenter: (show: boolean) => void;
  /** The roster line, from the renderer's read of the presented world. */
  readonly setRoster: (names: string[], total: number) => void;
  /** Starts the subscriptions that drive every field the core knows. */
  readonly bind: (core: Core) => void;
};

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
      nodes.who.textContent =
        total === 0
          ? "the park is empty"
          : `dogs here: ${names.join(", ")}${total > names.length ? ` +${total - names.length} more` : ""}`;
    },
    bind: (core) => {
      const checkinState = (): void => {
        const player = core.state.role === "player";
        nodes.checkin.style.display = player ? "" : "none";
        nodes.boost.style.display = player ? "" : "none";
        const checkedIn = core.state.checkedInToday;
        nodes.checkin.disabled = !player || !core.state.present || checkedIn;
        nodes.checkin.textContent = checkedIn ? "Checked in ✓" : "Check in";
      };

      core.subscribe("status", (status) => {
        connected = status === "connected";
        if (connected && diag.startedAt === 0) diag.startedAt = Date.now();
        setStatus(status);
      });
      core.subscribe("dialFailures", (n) => {
        // After a few straight failures, say so where the roster would be:
        // dials keep retrying, but the user should know the park is
        // unreachable rather than empty (iOS WebKit's WebTransport
        // networking crash looks exactly like this).
        if (n === UNREACHABLE_AFTER_DIALS) nodes.who.textContent = UNREACHABLE;
      });
      core.subscribe("role", (role) => {
        nodes.role.textContent = `${role} @ ${core.state.park}`;
        checkinState();
      });
      core.subscribe("present", checkinState);
      core.subscribe("checkedInToday", checkinState);
      core.subscribe("tick", (tick) => {
        diag.tick = Number(tick);
        nodes.tick.textContent = String(tick);
      });
      core.subscribe("rttMs", (ms) => {
        nodes.rtt.textContent = `${ms.toFixed(0)}ms`;
      });
      core.subscribe("worldOk", (ok) => {
        nodes.world.textContent = ok === null ? "–" : ok ? "✓" : "✗";
      });
      core.subscribe("parkEnergy", (energy) => {
        nodes.energy.textContent = String(energy);
      });
      core.subscribe("bytesDown", (bytes) => {
        diag.bytesDown = bytes;
        // The headline cost metric: downlink bytes per hour of screen-on
        // time, once there is enough screen-on time to divide by.
        const hours = (Date.now() - (diag.startedAt || Date.now())) / 3600000;
        nodes.bytes.textContent =
          hours > 0.001 ? `${(bytes / 1024 / hours).toFixed(1)}KB/h` : `${bytes}B`;
      });
      core.subscribe("events", (n) => {
        diag.events = n;
      });
      core.subscribe("rollbacks", (n) => {
        diag.rollbacks = n;
      });
      core.subscribe("resyncs", (n) => {
        diag.resyncs = n;
      });
      core.subscribe("checks", (n) => {
        diag.checks = n;
      });
      core.subscribe("mismatches", (n) => {
        diag.mismatches = n;
      });
      core.subscribe("rejects", (n) => {
        diag.rejects = n;
      });
    },
  };
}

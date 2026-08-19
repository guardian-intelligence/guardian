// The session core speaks a numeric telemetry vocabulary; the analytics
// pipeline speaks span names and attribute keys. This is the translation,
// and the span names and keys below are the dashboards' contract — the
// numbers on the left may change with the core, the strings on the right
// may not.

import { Emit, HostEmit, IntentDrop, ResyncReason, moduleWordHex } from "@guardian/chunkies";
import { actionName, type WumGame } from "@guardian/wum-client";
import { emitSpan, reportError } from "@guardian/telemetry";
import { rejectText } from "./hud";
import type { JankCounters } from "./jank";

// Why the replica asked for a snapshot, or — for the last two — tore the
// stream down instead. The dashboard groups `wum.why`, so these stay the
// sentences proto 3 emitted.
// The clock's dashboard vocabulary: the sim/clock crate's numbering, in
// the exact strings the dashboards group by.
const CLOCK_SPAN_NAMES = ["acquiring", "locked", "fast-forward", "snapshot-required"] as const;

const RESYNC_WHY: Record<number, string> = {
  [ResyncReason.clock]: "clock: beyond the recovery window",
  [ResyncReason.lateEvent]: "late event beyond rollback ring",
  [ResyncReason.eventRejected]: "event rejected locally",
  [ResyncReason.hashMismatch]: "world hash mismatch",
  [ResyncReason.checkAgedOut]: "check aged out of the server ring",
  [ResyncReason.moduleEpoch]: "module epoch",
  [ResyncReason.terrainFetch]: "terrain fetch failed",
  [ResyncReason.restoreFailed]: "snapshot restore failed",
  [ResyncReason.queueOverflow]: "event queue overflow",
  [ResyncReason.moduleSwapped]: "module swapped",
  [ResyncReason.streamOverflow]: "stream overflow",
  [ResyncReason.framing]: "unreadable frame length",
};

// Codes the mapping knows and deliberately does not beacon: per-event and
// per-check cadence facts whose volume would evict rarer spans from the
// bounded beacon queue (the debug and stats panes still see every code).
// Anything else the switch below doesn't name ships raw as `wum.emit`, so
// a new core emission is never silently unrecorded — merely unnamed until
// this mapping catches up.
const UNBEACONED: ReadonlySet<number> = new Set<number>([
  Emit.welcome,
  Emit.eventApplied,
  Emit.checkSent,
  Emit.verdict,
  Emit.moduleSwapped,
  Emit.intentSent,
  Emit.presence,
  Emit.replayed,
  Emit.intentResent,
]);

// Every span this module emits goes through one function, so a harness can
// watch the whole vocabulary by tapping one place. The tap is a read: it
// never replaces the emission, and nothing installs one outside `?probe=1`.
let tap: ((name: string, attrs: Record<string, string>) => void) | null = null;

/** Mirrors every span emitted from here to `fn`. Dev and harnesses only. */
export function tapSpans(fn: (name: string, attrs: Record<string, string>) => void): void {
  tap = fn;
}

function span(name: string, attrs: Record<string, string>): void {
  emitSpan(name, attrs);
  tap?.(name, attrs);
}

export type SignInFlow = "popup" | "redirect";

/**
 * A minute of feel, from every session that drew a frame. The counters and
 * their thresholds are defined in jank.ts; this is only their name on the
 * wire, and the play-test harness computes the same figures from the same
 * frames, so a regression the harness catches locally is the one this
 * reports from production.
 */
export function jank(park: string, c: JankCounters): void {
  span("wum.jank", {
    "wum.long_frames": String(c.longFrames),
    "wum.backward_ticks": String(c.backwardTicks),
    "wum.own_dog_jumps": String(c.ownDogJumps),
    "wum.freeze_runs": String(c.freezeRuns),
    "wum.park": park,
  });
}

export type Telemetry = {
  /** The `telemetry` injection: the session's numeric vocabulary, mapped to spans. */
  readonly emit: (code: number, a: bigint, b: bigint) => void;
  /** What the dial that just succeeded cost, for the connected span. */
  readonly noteDial: (dialMs: number, anon: boolean) => void;
  /** Subscribes to the connection lifecycle the game owns. */
  readonly bind: (game: WumGame) => void;
};

export function signedIn(flow: SignInFlow): void {
  span("wum.signin", { "wum.flow": flow });
}

export function signInFailed(reason: string, flow: SignInFlow): void {
  span("wum.signin_failed", { "wum.reason": reason, "wum.flow": flow });
}

export function unsupported(): void {
  span("wum.unsupported", { "wum.feature": "webtransport" });
}

/** Reports a boot failure and returns the error id to show the user. */
export function reportBootFailure(e: unknown): string {
  return reportError(e, { "error.op": "wum.boot" });
}

/** Reports the throw that stopped the frame loop. Once per session, by construction. */
export function reportFrameFailure(e: unknown): string {
  return reportError(e, { "error.op": "wum.frame" });
}

export function createTelemetry(ctx: {
  readonly park: string;
  readonly log: (line: string) => void;
}): Telemetry {
  const park = ctx.park;
  let role = "spectator";
  let anon = true;
  let dialMs = 0;
  let rateHz = 0;

  return {
    noteDial: (ms, wasAnon) => {
      dialMs = ms;
      anon = wasAnon;
    },
    bind: (game) => {
      game.subscribe((s) => {
        role = s.connection.role ?? role;
        rateHz = s.connection.rateHz ?? rateHz;
      });
    },
    emit: (code, a, b) => {
      switch (code) {
        case Emit.connectedHelloSent:
          span("wum.connected", {
            "wum.park": park,
            "wum.role": role,
            "wum.anon": String(anon),
            "wum.dial_ms": String(dialMs),
          });
          return;
        case Emit.resyncRequested:
          span("wum.netcode_resync", {
            "wum.why": whyOf(Number(a)),
            "wum.seq": String(b),
            "wum.park": park,
          });
          return;
        case Emit.mismatch:
          span("wum.netcode_mismatch", { "wum.tick": String(a), "wum.park": park });
          return;
        case Emit.intentAnswered: {
          // The action lifecycle, as finished facts the core measured:
          // this is the per-action latency dashboards rank kinds by.
          const kind = Number(a & 0xffffn);
          span("wum.action", {
            "wum.kind": actionName(kind) ?? `kind ${kind}`,
            "wum.ms": String(b),
            "wum.resends": String(a >> 16n),
            "wum.rate_hz": String(rateHz),
            "wum.park": park,
          });
          return;
        }
        case Emit.rateChanged: {
          const oldHz = Number(BigInt.asUintN(32, b >> 32n));
          const newHz = Number(BigInt.asUintN(32, b));
          // Host state is patched after the telemetry callback, so advance
          // the local dimension here before any following action finishes.
          rateHz = newHz;
          span("wum.tick_rate", {
            "wum.from_hz": String(oldHz),
            "wum.to_hz": String(newHz),
            "wum.tick": String(a),
            "wum.park": park,
          });
          return;
        }
        case Emit.intentDropped: {
          const kind = Number(a & 0xffffn);
          const why = Number(a >> 16n);
          span("wum.action_dropped", {
            "wum.kind": actionName(kind) ?? `kind ${kind}`,
            "wum.why": why === IntentDrop.overflow ? "overflow" : "reidentify",
            "wum.held_ms": String(b),
            "wum.park": park,
          });
          return;
        }
        case Emit.eventArrived:
          // Per accepted event, so this is the highest-volume span the
          // probe carries. It is what makes the authority's stamp-to-wire
          // delay measurable: margin is the event's tick minus where the
          // replica stood, and the delay is the trail minus that margin.
          span("wum.netcode_arrived", {
            "wum.tick": String(a),
            "wum.replica_tick": String(b),
            "wum.park": park,
          });
          return;
        case Emit.reject:
          ctx.log(rejectText(Number(a)));
          span("wum.netcode_reject", {
            "wum.reason": String(a),
            "wum.kind": String(b),
            "wum.park": park,
          });
          return;
        case Emit.autoRejoin:
          ctx.log("rejoining the park with your dog");
          return;
        case Emit.moduleSwapWanted:
          span("wum.module_swap", {
            "wum.hash": moduleWordHex(Number(a)),
            "wum.park": park,
          });
          return;
        case Emit.clockState:
          // Fires on state transitions only. The deficit is signed:
          // positive is behind the schedule, negative is ahead of it.
          span("wum.netcode_clock", {
            "wum.state": CLOCK_SPAN_NAMES[Number(a)] ?? `state ${a}`,
            "wum.err_ticks": String(BigInt.asIntN(64, b)),
            "wum.park": park,
          });
          return;
        case Emit.rollback:
          // rollback(returned_to, late << 32 | rewound). Three facts a
          // single "depth" used to blur: the absolute tick the repair
          // returned to, how late the event was (the defect signal), and
          // how far back the ring's cadence forced the reach (the repair
          // cost — usually larger, and the one a player watches).
          span("wum.netcode_rollback", {
            "wum.returned_to": String(a),
            "wum.late_ticks": String(b >> 32n),
            "wum.rewound_ticks": String(b & 0xffffffffn),
            "wum.park": park,
          });
          return;
        case Emit.snapshotRestored:
          span("wum.netcode_restore", {
            "wum.seq": String(a),
            "wum.tick": String(b),
            "wum.park": park,
          });
          return;
        case HostEmit.redial:
          span("wum.redial", {
            "wum.backoff_ms": String(a),
            "wum.attempt": String(b),
            "wum.park": park,
          });
          return;
        case HostEmit.teardown:
          span("wum.netcode_teardown", {
            "wum.why": whyOf(Number(a)),
            "wum.park": park,
          });
          return;
        case Emit.restoreFailed: {
          const id = reportError(new Error(`snapshot restore failed (code ${a})`), {
            "error.op": "wum.snapshot_restore",
          });
          ctx.log(`the park's state didn't land (code ${a}) [err ${id}]`);
          return;
        }
        default:
          if (UNBEACONED.has(code)) return;
          span("wum.emit", {
            "wum.code": String(code),
            "wum.a": String(a),
            "wum.b": String(b),
            "wum.park": park,
          });
          return;
      }
    },
  };
}

function whyOf(reason: number): string {
  return RESYNC_WHY[reason] ?? `reason ${reason}`;
}

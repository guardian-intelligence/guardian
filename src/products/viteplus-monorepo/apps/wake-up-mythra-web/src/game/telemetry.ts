// The session core speaks a numeric telemetry vocabulary; the analytics
// pipeline speaks span names and attribute keys. This is the translation,
// and the span names and keys below are the dashboards' contract — the
// numbers on the left may change with the core, the strings on the right
// may not.

import {
  Emit,
  HostEmit,
  ResyncReason,
  moduleWordHex,
  type Core,
} from "@guardian/mythrad-client-core";
import { emitSpan, reportError } from "@guardian/telemetry";
import { rejectText } from "./hud";
import type { JankCounters } from "./jank";

// Why the replica asked for a snapshot, or — for the last two — tore the
// stream down instead. The dashboard groups `wum.why`, so these stay the
// sentences proto 3 emitted.
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

// Clock states as the sim/clock crate numbers them; the span carries the
// name so dashboards never depend on the numeric order.
const CLOCK_STATES = ["acquiring", "locked", "fast-forward", "snapshot-required"] as const;

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
  /** The `telemetry` port: the core's numeric vocabulary, mapped to spans. */
  readonly emit: (code: number, a: bigint, b: bigint) => void;
  /** What the dial that just succeeded cost, for the connected span. */
  readonly noteDial: (dialMs: number, anon: boolean) => void;
  /** Subscribes to the connection lifecycle the core owns. */
  readonly bind: (core: Core) => void;
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

export function createTelemetry(ctx: {
  readonly park: string;
  readonly log: (line: string) => void;
}): Telemetry {
  const park = ctx.park;
  let role = "spectator";
  let anon = true;
  let dialMs = 0;

  return {
    noteDial: (ms, wasAnon) => {
      dialMs = ms;
      anon = wasAnon;
    },
    bind: (core) => {
      core.subscribe("role", (r) => {
        role = r;
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
            "wum.state": CLOCK_STATES[Number(a)] ?? `state ${a}`,
            "wum.err_ticks": String(BigInt.asIntN(64, b)),
            "wum.park": park,
          });
          return;
        case Emit.rollback:
          // Two different quantities that a single "depth" used to blur:
          // how late the event was, and how far back the ring's cadence
          // forced the repair to reach to apply it. The second is usually
          // the larger, and it is the one a player watches.
          span("wum.netcode_rollback", {
            "wum.late_ticks": String(a),
            "wum.rewound_ticks": String(b),
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
          span("wum.redial", { "wum.backoff_ms": String(a), "wum.park": park });
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
          return;
      }
    },
  };
}

function whyOf(reason: number): string {
  return RESYNC_WHY[reason] ?? `reason ${reason}`;
}

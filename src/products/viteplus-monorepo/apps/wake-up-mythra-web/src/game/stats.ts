// Stats for Nerds: the session's diagnostics record, on screen, in prod.
// The one number it exists for is the trail — how far this browser's
// world runs behind the authority's present — shown against the target
// the module itself states (the 1-tick invariant rides the record, so
// this pane can never disagree with the netcode about the goal).
//
// Everything here is a read: the record is polled a few times a second
// and discarded, and the two emit codes the pane listens to feed small
// rings kept only for on-screen medians. Nothing is a second telemetry
// path — the spans leave through telemetry.ts exactly as before.
//
// Visible with `?stats=1`, or toggled any time with the backquote key.

import { ActionKind, Emit, type Core } from "@guardian/mythrad-client-core";

const CLOCK_STATES = ["acquiring", "locked", "fast-forward", "snapshot-required"] as const;
const RING = 128;
const REFRESH_MS = 250;

export type StatsPane = {
  /** Fed the core's telemetry stream, same tap as the other consumers. */
  readonly onEmit: (code: number, a: bigint, b: bigint) => void;
};

function median(xs: readonly number[]): number | null {
  if (xs.length === 0) return null;
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.floor(s.length / 2)]!;
}

export function createStatsPane(core: Core): StatsPane {
  const margins: number[] = [];
  const actions = new Map<number, number[]>();

  const panel = document.createElement("div");
  panel.style.cssText =
    "position:fixed;top:64px;right:8px;background:#161a21ee;color:#f4f1ea;" +
    "font:12px ui-monospace,monospace;padding:10px 12px;border:1px solid #333;" +
    "border-radius:8px;z-index:9;min-width:260px;white-space:pre;display:none";
  document.body.appendChild(panel);

  let visible = new URLSearchParams(location.search).has("stats");
  const apply = (): void => {
    panel.style.display = visible ? "block" : "none";
  };
  apply();
  window.addEventListener("keydown", (e) => {
    if (e.key === "`" && !(e.target instanceof HTMLInputElement)) {
      visible = !visible;
      apply();
    }
  });

  setInterval(() => {
    if (!visible) return;
    const d = core.diag();
    if (!d) {
      panel.textContent = "no session yet";
      return;
    }
    const state = core.state;
    const lines = [
      `trail   ${d.trailTicks.toFixed(1)} ticks (target ≤${d.trailTargetTicks}, cushion ${d.cushionTicks})`,
      `clock   ${CLOCK_STATES[d.clockState] ?? "?"} · rtt ${d.rttMs}ms · ${state.hz}Hz`,
      `world   tick ${d.tick} · seq ${d.seq}`,
      `repairs ${d.rollbacks} rollbacks · ${d.resyncs} resyncs · ${d.mismatches} mismatches`,
      `events  ${d.events} applied · ${d.rejects} rejected`,
      `arrive  margin p50 ${median(margins)?.toFixed(0) ?? "—"} ticks (${margins.length})`,
    ];
    for (const [kind, ms] of actions) {
      const name = ActionKind[kind] ?? `kind ${kind}`;
      lines.push(
        `action  ${name} ×${ms.length} · last ${ms[ms.length - 1]}ms · p50 ${median(ms)?.toFixed(0)}ms`,
      );
    }
    panel.textContent = lines.join("\n");
  }, REFRESH_MS);

  return {
    onEmit: (code, a, b) => {
      if (code === Emit.eventArrived) {
        // arrived(event tick, replica tick): positive margin = the event
        // beat the replica to its own tick; negative = a repair was owed
        margins.push(Number(a - b));
        if (margins.length > RING) margins.shift();
      } else if (code === Emit.intentAnswered) {
        const kind = Number(a & 0xffffn);
        const ring = actions.get(kind) ?? [];
        ring.push(Number(b));
        if (ring.length > RING) ring.shift();
        actions.set(kind, ring);
      }
    },
  };
}

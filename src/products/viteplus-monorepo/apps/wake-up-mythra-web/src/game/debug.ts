// Dev-only: the clock's account of itself, and a way to starve it.
// Freezing pumps the core with a zero step budget, so the clock still
// observes and escalates and events still apply while nothing steps —
// the deficit grows, the state walks to fast-forward and on to
// snapshot-required, and the recovery after resume all happen on screen.

import { CLOCK_STATE_NAMES, Emit, type Core } from "@guardian/mythrad-client-core";
import * as v from "valibot";

export type DebugPanel = {
  /** True while step execution is starved; the frame loop pumps a zero budget. */
  readonly frozen: () => boolean;
  /** Fed the core's telemetry so the panel can narrate verdicts. */
  readonly onEmit: (code: number, a: bigint, b: bigint) => void;
  readonly update: () => void;
};

export function createDebugPanel(core: Core): DebugPanel {
  const panel = document.createElement("div");
  panel.style.cssText =
    "position:fixed;bottom:8px;right:8px;background:#161a21ee;color:#f4f1ea;" +
    "font:12px ui-monospace,monospace;padding:10px 12px;border:1px solid #333;" +
    "border-radius:8px;z-index:9;min-width:240px";
  panel.innerHTML = `<div style="opacity:.6;margin-bottom:6px">dev debug — clock</div>
    <div id="dbg-clock">clock —</div>
    <div id="dbg-desync">desync —</div>
    <div id="dbg-replica">replica —</div>
    <div id="dbg-verdict">verdict —</div>
    <div style="margin-top:8px;display:flex;gap:6px;flex-wrap:wrap">
      <button id="dbg-freeze8">freeze 8s</button>
      <button id="dbg-freeze40">freeze 40s</button>
      <button id="dbg-resume">resume</button>
    </div>`;
  document.body.appendChild(panel);

  const line = (id: string): HTMLElement =>
    v.parse(v.instance(HTMLElement), panel.querySelector(`#${id}`));
  const button = (id: string): HTMLButtonElement =>
    v.parse(v.instance(HTMLButtonElement), panel.querySelector(`#${id}`));

  let freezeUntil = 0;
  let verdict = "—";
  const frozen = (): boolean => Date.now() < freezeUntil;

  button("dbg-freeze8").onclick = () => {
    freezeUntil = Date.now() + 8000;
  };
  button("dbg-freeze40").onclick = () => {
    // past the ring: watch the clock give up on stepping and demand a snapshot
    freezeUntil = Date.now() + 40000;
  };
  button("dbg-resume").onclick = () => {
    freezeUntil = 0;
  };

  return {
    frozen,
    onEmit: (code, a, b) => {
      if (code === Emit.verdict) {
        verdict = `${a === 0n ? "MISMATCH ✗" : "ok ✓"} · ${b}ms round trip`;
      }
    },
    update: () => {
      const state = core.state;
      const d = core.diag();
      if (!d) return;
      line("dbg-clock").textContent =
        `clock ${CLOCK_STATE_NAMES[d.clockState] ?? "?"}${frozen() ? " (frozen)" : ""}` +
        ` · rtt ${d.rttMs}ms`;
      const seconds = state.hz > 0 ? ` (${(d.errorTicks / state.hz).toFixed(2)}s)` : "";
      line("dbg-desync").textContent = `desync ${d.errorTicks.toFixed(1)} ticks${seconds}`;
      line("dbg-replica").textContent =
        `replica tick ${d.tick} seq ${d.seq}` + ` · ${d.rollbacks} rollbacks, ${d.resyncs} resyncs`;
      line("dbg-verdict").textContent = `verdict ${verdict}`;
    },
  };
}

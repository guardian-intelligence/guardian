import { tuning } from "../studio/store.ts";
import { createFlame, type FlameState } from "./flame.ts";

// Rumi's light: one candle flame shared by everything she lights (her own
// glow and the message field's shine), stepped once per animation frame while
// anyone is listening.

type Listener = (s: FlameState) => void;

const flame = createFlame();
let state: FlameState = { brightness: 1, size: 1, swayX: 0, swayY: 0 };
const listeners = new Set<Listener>();
let raf = 0;
let last = 0;

function tick(now: number): void {
  raf = requestAnimationFrame(tick);
  const dt = last === 0 ? 0 : Math.min(0.1, Math.max(0, (now - last) / 1000));
  last = now;
  const o = tuning.get().orb;
  const still = matchMedia("(prefers-reduced-motion: reduce)").matches;
  state = flame(still ? 0 : dt, {
    waver: o.flameWaver,
    flicker: o.flameFlicker,
    rate: o.flameRate,
    gust: o.flameGust,
  });
  listeners.forEach((l) => l(state));
}

export const light = {
  get: (): FlameState => state,
  subscribe: (listener: Listener): (() => void) => {
    listeners.add(listener);
    if (listeners.size === 1) {
      last = 0;
      raf = requestAnimationFrame(tick);
    }
    return () => {
      listeners.delete(listener);
      if (listeners.size === 0) cancelAnimationFrame(raf);
    };
  },
};

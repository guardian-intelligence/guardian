import { emissionAt } from "../rumi/emission.ts";

// The room is dark metal and the Recently glyphs are etched into it: matte in
// plain reading, catching only a faint grainy sheen from Rumi. She is a small
// light in a vast room, so it falls off with distance from her rim; the widget
// sits a few hundred px away and gets the tail of it. `.etch` paints it.

const REACH = 200;

/**
 * Rumi's light on every `.etch` glyph under `root`: her centre in the glyph's
 * box (--ex/--ey, px), the direction it travels across it (--edx/--edy, unit,
 * away from her), how far it carries past her rim (--etch-reach, css px; the
 * sheen falls off as e^(-d/reach)) and the colour her face toward the glyph
 * emits (--etch-rgb).
 */
export function lightEtchings(root: HTMLElement, x: number, y: number): void {
  const r = root.getBoundingClientRect();
  root.querySelectorAll<HTMLElement>(".etch").forEach((el) => {
    const b = el.getBoundingClientRect();
    if (b.width === 0 || b.height === 0) return;
    const ex = x - (b.left - r.left);
    const ey = y - (b.top - r.top);
    const nx = Math.min(Math.max(ex, 0), b.width) - ex;
    const ny = Math.min(Math.max(ey, 0), b.height) - ey;
    const len = Math.hypot(nx, ny) || 1;
    const s = el.style;
    s.setProperty("--ex", `${ex.toFixed(1)}px`);
    s.setProperty("--ey", `${ey.toFixed(1)}px`);
    s.setProperty("--edx", (nx / len).toFixed(3));
    s.setProperty("--edy", (ny / len).toFixed(3));
    s.setProperty("--etch-reach", `${REACH}px`);
    const face = emissionAt(Math.atan2(-ny, nx));
    if (face) s.setProperty("--etch-rgb", face);
  });
}

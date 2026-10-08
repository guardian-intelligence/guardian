import { useLayoutEffect, useRef, type CSSProperties, type ReactNode } from "react";

import type { GlassShape } from "../gpu/scene.ts";
import { emissionAt } from "../rumi/emission.ts";

// A Liquid Glass surface. The element itself only blurs the page beneath it
// (CSS backdrop-filter); the glass optics are drawn by the scene's overlay
// pass, which reads every registered element's box each frame.

type Entry = { el: HTMLElement; radius: number | "capsule" };
const registry = new Set<Entry>();

export function readShapes(root: HTMLElement): GlassShape[] {
  const r = root.getBoundingClientRect();
  const out: GlassShape[] = [];
  registry.forEach(({ el, radius }) => {
    if (!root.contains(el)) return;
    const b = el.getBoundingClientRect();
    if (b.width === 0 || b.height === 0) return;
    const hw = b.width / 2;
    const hh = b.height / 2;
    out.push({
      cx: b.left - r.left + hw,
      cy: b.top - r.top + hh,
      hw,
      hh,
      radius: radius === "capsule" ? Math.min(hw, hh) : radius,
      capsule: radius === "capsule",
    });
  });
  return out;
}


/**
 * Rumi's light reflecting onto every glass element: each gets the light's
 * position in its own box (--lx/--ly, px), the direction it comes from
 * (--ldx/--ldy, unit), how much reaches it (--lit, 0..1) and its colour
 * (--sun-rgb). The light is her glow off a wall `reach` css px behind her, so
 * it falls off as in ink.wgsl, (1 + (d/reach)^2)^-1.5 with d the distance to
 * the element's nearest edge, and its colour is her average leaning toward
 * the face of her turned that way (emission.ts). `.glass::after` paints it.
 */
export function lightGlass(root: HTMLElement, x: number, y: number, reach: number): void {
  const r = root.getBoundingClientRect();
  registry.forEach(({ el }) => {
    if (!root.contains(el)) return;
    const b = el.getBoundingClientRect();
    const lx = x - (b.left - r.left);
    const ly = y - (b.top - r.top);
    const nx = Math.min(Math.max(lx, 0), b.width) - lx;
    const ny = Math.min(Math.max(ly, 0), b.height) - ly;
    const cx = lx - b.width / 2;
    const cy = ly - b.height / 2;
    const len = Math.hypot(cx, cy) || 1;
    const s = el.style;
    s.setProperty("--lx", `${lx.toFixed(1)}px`);
    s.setProperty("--ly", `${ly.toFixed(1)}px`);
    s.setProperty("--ldx", (cx / len).toFixed(3));
    s.setProperty("--ldy", (cy / len).toFixed(3));
    const lit = (1 + (Math.hypot(nx, ny) / reach) ** 2) ** -1.5;
    s.setProperty("--lit", lit.toFixed(3));
    const face = emissionAt(Math.atan2(-ny, nx));
    if (face) s.setProperty("--sun-rgb", face);
  });
}

type Props = {
  radius: number | "capsule";
  className?: string;
  style?: CSSProperties;
  children?: ReactNode;
};

export function Glass({ radius, className, style, children }: Props) {
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const entry = { el, radius };
    registry.add(entry);
    return () => {
      registry.delete(entry);
    };
  }, [radius]);
  return (
    <div
      ref={ref}
      className={className ? `glass ${className}` : "glass"}
      style={{ borderRadius: radius === "capsule" ? 9999 : radius, ...style }}
    >
      {children}
    </div>
  );
}

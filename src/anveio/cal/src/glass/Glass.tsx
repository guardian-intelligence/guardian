import { useLayoutEffect, useRef, type CSSProperties, type ReactNode } from "react";

import type { GlassShape } from "../gpu/scene.ts";

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

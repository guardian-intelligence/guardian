import { useEffect, useRef, useState } from "react";

import {
  BUFFER,
  canvasFormat,
  drawFullscreen,
  webgpuContext,
  compilePipeline,
  getDevice,
  hexToRgb,
  reducedMotion,
} from "../gpu/gpu.ts";
import orbShader from "../shaders/orb.wgsl?raw";
import { tuning, useTuning } from "../studio/store.ts";

export type OrbMode = "idle" | "listening" | "thinking" | "speaking";

type Props = { mode: OrbMode; muted: boolean; size: number };

// The orb canvas is drawn a little larger than her circle so the antialiased rim isn't clipped.
const BLEED = 1.12;

export function Orb({ mode, muted, size }: Props) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const live = useRef({ mode, muted });
  live.current = { mode, muted };
  const [gpu, setGpu] = useState(false);
  const t = useTuning().orb;

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    let raf = 0;
    let dead = false;
    let cleanup = () => {};
    void getDevice().then(async (device) => {
      if (!device || dead) return;
      const pipeline = await compilePipeline(device, orbShader, canvasFormat(), "orb.wgsl").catch(
        (err: unknown) => {
          console.error(err);
          return null;
        },
      );
      if (!pipeline || dead) return;
      const ctx = webgpuContext(canvas);
      if (!ctx) return;
      ctx.configure({ device, format: canvasFormat(), alphaMode: "premultiplied" });
      const buffer = device.createBuffer({ size: 6 * 16, usage: BUFFER.UNIFORM | BUFFER.COPY_DST });
      const group = device.createBindGroup({
        layout: pipeline.getBindGroupLayout(0),
        entries: [{ binding: 0, resource: { buffer } }],
      });
      const a = {
        last: performance.now() / 1000,
        energy: 0.15,
        phase: 0,
        swirl: 0,
        mute: live.current.muted ? 1 : 0,
      };
      let visible = true;
      const io = new IntersectionObserver((e) => {
        visible = e[0]?.isIntersecting ?? true;
      });
      io.observe(canvas);

      const tick = () => {
        raf = requestAnimationFrame(tick);
        if (!visible) return;
        const dpr = Math.min(devicePixelRatio, 2);
        const px = Math.round(canvas.clientWidth * dpr);
        if (px === 0) return;
        if (canvas.width !== px) {
          canvas.width = px;
          canvas.height = px;
        }
        const o = tuning.get().orb;
        const { mode: m, muted: mu } = live.current;
        const now = reducedMotion() ? 3 : performance.now() / 1000;
        const dt = Math.min(0.1, Math.max(0, now - a.last));
        a.last = now;
        const base = {
          idle: o.energyIdle,
          listening: o.energyListen,
          thinking: o.energyThink,
          speaking: o.energySpeak,
        }[m];
        let target = base;
        if (m === "speaking")
          target *= 0.7 + 0.35 * Math.abs(Math.sin(now * 6.1) * Math.sin(now * 2.3 + 0.5));
        if (m === "listening")
          target *= 0.7 + 0.4 * Math.abs(Math.sin(now * 5.3) * Math.sin(now * 1.7 + 0.4));
        a.energy += (target - a.energy) * Math.min(1, dt * 6);
        const dir = m === "speaking" ? 1 : m === "listening" ? -0.7 : 0.25;
        a.phase += dt * Math.PI * 2 * o.rippleSpeed * dir;
        a.swirl += dt * (m === "thinking" ? o.swirl : 0.04);
        a.mute += ((mu ? 1 : 0) - a.mute) * Math.min(1, dt * 6);
        device.queue.writeBuffer(
          buffer,
          0,
          new Float32Array([
            px,
            px,
            now,
            0,
            a.energy,
            a.phase,
            a.swirl,
            a.mute,
            ...hexToRgb(o.accent),
            1,
            ...hexToRgb(o.soft),
            1,
            ...hexToRgb(o.blush),
            1,
            o.gloss,
            o.rippleDensity,
            now * o.drift,
            o.muteDrain,
          ]),
        );
        const enc = device.createCommandEncoder();
        drawFullscreen(enc, ctx.getCurrentTexture().createView(), pipeline, group);
        device.queue.submit([enc.finish()]);
        setGpu(true);
      };
      tick();
      cleanup = () => {
        io.disconnect();
        buffer.destroy();
        ctx.unconfigure();
      };
    });
    return () => {
      dead = true;
      cancelAnimationFrame(raf);
      cleanup();
    };
    // orbShader is a dep so editing orb.wgsl rebuilds the pipeline on hot reload
  }, [orbShader]);

  return (
    <span
      className="orb"
      style={{ width: size, height: size, animationDuration: `${(1 / t.breatheHz).toFixed(2)}s` }}
      data-mode={mode}
    >
      <span
        className="orb-flat"
        aria-hidden="true"
        style={{
          opacity: gpu ? 0 : 1,
          background: `radial-gradient(circle at 38% 32%, #fff 0%, ${t.soft} 28%, ${t.accent} 72%, ${t.blush} 100%)`,
          filter: muted ? "saturate(.12) brightness(1.08)" : "none",
        }}
      />
      <canvas
        ref={canvasRef}
        aria-hidden="true"
        className="orb-gpu"
        style={{
          left: `${(-(BLEED - 1) / 2) * 100}%`,
          top: `${(-(BLEED - 1) / 2) * 100}%`,
          width: `${BLEED * 100}%`,
          height: `${BLEED * 100}%`,
          opacity: gpu ? 1 : 0,
        }}
      />
    </span>
  );
}

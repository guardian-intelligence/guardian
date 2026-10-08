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
import { light } from "./light.ts";

export type OrbMode = "idle" | "listening" | "thinking" | "speaking";

type Props = { mode: OrbMode; muted: boolean; size: number };

// Per state: paint-wave direction (+ out, - in) and strength, how much the
// smear warps, and swirl speed (rad/s; working multiplies it by the studio's swirl).
const PAINT_STATES = {
  idle: { dir: 0.15, wave: 0, warp: 1, swirl: 0.03 },
  listening: { dir: -1, wave: 0.6, warp: 1, swirl: 0.05 },
  thinking: { dir: 0.3, wave: 0.15, warp: 1.35, swirl: 1 },
  speaking: { dir: 1, wave: 1, warp: 1.1, swirl: 0.06 },
} as const satisfies Record<OrbMode, { dir: number; wave: number; warp: number; swirl: number }>;

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
      const buffer = device.createBuffer({
        size: 10 * 16,
        usage: BUFFER.UNIFORM | BUFFER.COPY_DST,
      });
      const group = device.createBindGroup({
        layout: pipeline.getBindGroupLayout(0),
        entries: [{ binding: 0, resource: { buffer } }],
      });
      // Eased copies of the state targets, plus the integrated clocks the paint
      // moves on (integrated so retuning a speed never makes the paint jump).
      const a = {
        last: performance.now() / 1000,
        stir: 0.25,
        wave: 0,
        warp: 1,
        swirlRate: 0.03,
        paintTime: 0,
        swirl: 0,
        phase: 0,
        mute: live.current.muted ? 1 : 0,
      };
      let visible = true;
      const io = new IntersectionObserver((e) => {
        visible = e[0]?.isIntersecting ?? true;
      });
      io.observe(canvas);
      // keep the shared candle burning while she's on screen
      const unlight = light.subscribe(() => {});

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
        const now = performance.now() / 1000;
        const dt = reducedMotion() ? 0 : Math.min(0.1, Math.max(0, now - a.last));
        a.last = now;

        // Rumi's state machine: each state sets how hard the paint is stirred,
        // which way paint waves travel, how much the smear warps, how fast it swirls.
        const target = PAINT_STATES[m];
        const stir = {
          idle: o.energyIdle,
          listening: o.energyListen,
          thinking: o.energyThink,
          speaking: o.energySpeak,
        }[m];
        const k = Math.min(1, dt * o.ease);
        a.stir += (stir - a.stir) * k;
        a.wave += (target.wave - a.wave) * k;
        a.warp += (target.warp - a.warp) * k;
        a.swirlRate += (target.swirl * (m === "thinking" ? o.swirl : 1) - a.swirlRate) * k;
        a.paintTime += dt * (0.12 + a.stir);
        a.swirl += dt * a.swirlRate;
        a.phase += dt * Math.PI * 2 * o.rippleSpeed * target.dir;
        a.mute += ((mu ? 1 : 0) - a.mute) * Math.min(1, dt * 3);
        const f = light.get();

        device.queue.writeBuffer(
          buffer,
          0,
          new Float32Array([
            px,
            px,
            now,
            o.matte,
            a.paintTime,
            a.swirl,
            a.phase,
            a.wave,
            o.smearScale,
            o.warp * a.warp,
            o.sharpness,
            a.mute,
            ...hexToRgb(o.green),
            o.greenAmount,
            ...hexToRgb(o.red),
            o.redAmount,
            ...hexToRgb(o.indigo),
            o.indigoAmount,
            ...hexToRgb(o.glow),
            o.glowStrength * (1 - 0.7 * a.mute),
            f.brightness,
            f.size,
            f.swayX,
            f.swayY,
            o.blur,
            o.bezel,
            o.refIndex,
            o.dispersion,
            o.glare,
            o.fresnel,
            o.glowRadius,
            0,
          ]),
        );
        const enc = device.createCommandEncoder();
        drawFullscreen(enc, ctx.getCurrentTexture().createView(), pipeline, group);
        device.queue.submit([enc.finish()]);
        setGpu(true);
      };
      tick();
      cleanup = () => {
        unlight();
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
          background: `radial-gradient(circle at 36% 34%, ${t.glow} 0%, ${t.green} 34%, ${t.indigo} 70%, ${t.red} 100%)`,
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

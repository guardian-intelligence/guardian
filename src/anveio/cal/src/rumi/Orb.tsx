import { useEffect, useRef, useState } from "react";

import { DESIGN } from "../design.ts";
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
import { emissionTexture, readEmission } from "./emission.ts";

export type OrbMode = "idle" | "listening" | "thinking" | "speaking";

type Props = { mode: OrbMode; muted: boolean; size: number };

// Per state (see orb.wgsl): how fast the droplets drift (1 = one full
// rearrangement in about 30 s), how far the microscope's focus is pulled in,
// how much they gather and merge, and whether her brightness follows her voice.
const STATES = {
  idle: { drift: 1, focus: 0.35, gather: 0, voice: 0 },
  listening: { drift: 0.8, focus: 0.75, gather: 0, voice: 0 },
  thinking: { drift: 2.4, focus: 0.45, gather: 1, voice: 0 },
  speaking: { drift: 1, focus: 0.5, gather: 0, voice: 1 },
} as const satisfies Record<OrbMode, { drift: number; focus: number; gather: number; voice: number }>;

// At rest the focus breathes, sharpening and softening every 7 s.
const BREATH_SECONDS = 7;
const BREATH_DEPTH = 0.18;
// How quickly she eases between states (per second); always soft.
const EASE = 0.9;

// The orb canvas is drawn a little larger than her circle so the antialiased
// rim isn't clipped; orb.wgsl's VIEW must match.
const BLEED = 1.12;

export function Orb({ mode, muted, size }: Props) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const live = useRef({ mode, muted });
  live.current = { mode, muted };
  const [gpu, setGpu] = useState(false);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    let raf = 0;
    let dead = false;
    let cleanup = () => {};
    void getDevice().then(async (device) => {
      if (!device || dead) return;
      // her view, and the light she throws on the wall behind her (emission.ts)
      const [pipeline, emitter] = await Promise.all([
        compilePipeline(device, orbShader, canvasFormat(), "orb.wgsl"),
        compilePipeline(device, orbShader, "rgba8unorm", "orb.wgsl", "fs_emission"),
      ]).catch((err: unknown) => {
        console.error(err);
        return [null, null] as const;
      });
      if (!pipeline || !emitter || dead) return;
      const emission = emissionTexture(device).createView();
      const ctx = webgpuContext(canvas);
      if (!ctx) return;
      ctx.configure({ device, format: canvasFormat(), alphaMode: "premultiplied" });
      const buffer = device.createBuffer({
        size: 8 * 16,
        usage: BUFFER.UNIFORM | BUFFER.COPY_DST,
      });
      const group = device.createBindGroup({
        layout: pipeline.getBindGroupLayout(0),
        entries: [{ binding: 0, resource: { buffer } }],
      });
      const emitGroup = device.createBindGroup({
        layout: emitter.getBindGroupLayout(0),
        entries: [{ binding: 0, resource: { buffer } }],
      });
      // Eased copies of the state targets, plus the clock the droplets drift on
      // (integrated so a state change never makes them jump).
      const a = {
        last: performance.now() / 1000,
        drift: 1,
        focus: 0.35,
        gather: 0,
        voice: 0,
        driftTime: 0,
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
        const o = DESIGN.orb;
        const { mode: m, muted: mu } = live.current;
        const now = performance.now() / 1000;
        const still = reducedMotion();
        const dt = Math.min(0.1, Math.max(0, now - a.last));
        a.last = now;

        const target = STATES[m];
        const k = Math.min(1, dt * EASE);
        a.drift += (target.drift - a.drift) * k;
        a.focus += (target.focus - a.focus) * k;
        a.gather += (target.gather - a.gather) * k;
        a.voice += (target.voice - a.voice) * k;
        a.mute += ((mu ? 1 : 0) - a.mute) * Math.min(1, dt * 3);
        // reduced motion: a frozen frame that keeps only the focus breathing
        if (!still) a.driftTime += dt * a.drift;
        const breath = Math.sin((now * 2 * Math.PI) / BREATH_SECONDS) * BREATH_DEPTH * (1 - a.gather);
        const focus = Math.min(1, Math.max(0, a.focus + breath));
        // speaking: a soft swell standing in for her voice's loudness
        const voiced = still ? 0 : a.voice * (0.5 + 0.5 * Math.sin(now * 5.1) * Math.sin(now * 1.7));

        device.queue.writeBuffer(
          buffer,
          0,
          new Float32Array([
            px,
            px,
            now,
            focus,
            a.driftTime,
            a.gather,
            voiced,
            a.mute,
            ...hexToRgb(o.cream),
            0,
            ...hexToRgb(o.butter),
            0,
            ...hexToRgb(o.sage),
            0,
            ...hexToRgb(o.teal),
            0,
            ...hexToRgb(o.rim),
            o.rimOpacity,
            o.fringe,
            0,
            0,
            0,
          ]),
        );
        const enc = device.createCommandEncoder();
        drawFullscreen(enc, ctx.getCurrentTexture().createView(), pipeline, group);
        drawFullscreen(enc, emission, emitter, emitGroup);
        const collect = readEmission(device, enc);
        device.queue.submit([enc.finish()]);
        collect?.();
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
      style={{ width: size, height: size }}
      data-mode={mode}
    >
      <span
        className="orb-flat"
        aria-hidden="true"
        style={{
          opacity: gpu ? 0 : 1,
          // a still frame of her (public/rumi.png) until her shader is drawing, or without WebGPU
          background: "center / cover url(/rumi.png)",
          filter: muted ? "saturate(.7) brightness(.75)" : "none",
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

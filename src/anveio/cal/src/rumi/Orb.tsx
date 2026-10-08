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
import steamShader from "../shaders/steam.wgsl?raw";
import { emissionTexture, readEmission } from "./emission.ts";
import { Steam, type SteamParams } from "./steam.ts";

export type OrbMode = "idle" | "listening" | "thinking" | "speaking";

type Props = { mode: OrbMode; muted: boolean; size: number };

// Per state (see orb.wgsl): how hard the wind blows the steam (1 = DESIGN.orb.driftSpeed),
// which depth the microscope is focused at (-1 deepest, 1 nearest), and
// whether her brightness follows her voice.
const STATES = {
  idle: { drift: 1, focus: -0.7, voice: 0 },
  listening: { drift: 0.7, focus: -0.5, voice: 0 },
  thinking: { drift: 2.2, focus: -0.8, voice: 0 },
  speaking: { drift: 1, focus: -0.65, voice: 1 },
} as const satisfies Record<OrbMode, { drift: number; focus: number; voice: number }>;

// How much of the steam's grid one orb radius spans (orb.wgsl's SPAN), to
// turn the wind's speed in orb radii into grid units.
const SPAN = 0.36;

// The steam's physics for a given wind speed (orb radii/s) along DESIGN.orb.driftAngle.
function steamParams(speed: number): SteamParams {
  const o = DESIGN.orb;
  const a = (o.driftAngle * Math.PI) / 180;
  return {
    wind: [Math.cos(a) * speed * SPAN, -Math.sin(a) * speed * SPAN],
    settle: o.steamSettle,
    buoyancy: o.steamBuoyancy,
    diffusion: o.steamDiffusion,
    fading: o.steamFading,
    swirl: o.steamSwirl,
    feed: o.steamFeed,
  };
}

// At rest the focus breathes: the focal plane drifts up and down every 7 s.
const BREATH_SECONDS = 7;
const BREATH_DEPTH = 0.25;
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
      const steam = await Steam.create(
        device,
        steamShader,
        steamParams(DESIGN.orb.driftSpeed),
      ).catch((err: unknown) => {
        console.error(err);
        return null;
      });
      if (!steam || dead) {
        steam?.dispose();
        return;
      }
      const emission = emissionTexture(device).createView();
      const ctx = webgpuContext(canvas);
      if (!ctx) return;
      ctx.configure({ device, format: canvasFormat(), alphaMode: "premultiplied" });
      const buffer = device.createBuffer({
        size: 5 * 16,
        usage: BUFFER.UNIFORM | BUFFER.COPY_DST,
      });
      const wrap = device.createSampler({
        magFilter: "linear",
        minFilter: "linear",
        addressModeU: "repeat",
        addressModeV: "repeat",
      });
      // one bind group per steam buffer, since the steam ping-pongs between two
      const groupsFor = (p: GPURenderPipeline) =>
        steam.gas.map((view) =>
          device.createBindGroup({
            layout: p.getBindGroupLayout(0),
            entries: [
              { binding: 0, resource: { buffer } },
              { binding: 1, resource: view },
              { binding: 2, resource: wrap },
            ],
          }),
        );
      const groups = groupsFor(pipeline);
      const emitGroups = groupsFor(emitter);
      // Eased copies of the state targets.
      const a = {
        last: performance.now() / 1000,
        drift: 1,
        focus: -0.7,
        voice: 0,
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
        a.voice += (target.voice - a.voice) * k;
        a.mute += ((mu ? 1 : 0) - a.mute) * Math.min(1, dt * 3);
        // the wind, pulsing in one gentle rhythm; with reduced motion the steam
        // holds still and only the focus breathes
        const rhythm = 1 + o.pulse * Math.sin((now * 2 * Math.PI) / o.rhythmSeconds);
        const enc = device.createCommandEncoder();
        steam.step(enc, still ? 0 : dt, steamParams(o.driftSpeed * a.drift * rhythm));
        const breath = Math.sin((now * 2 * Math.PI) / BREATH_SECONDS) * BREATH_DEPTH;
        const focus = Math.min(1, Math.max(-1, a.focus + breath));
        // speaking: a soft swell standing in for her voice's loudness
        const voiced = still
          ? 0
          : a.voice * (0.5 + 0.5 * Math.sin(now * 5.1) * Math.sin(now * 1.7));

        device.queue.writeBuffer(
          buffer,
          0,
          new Float32Array([
            px,
            px,
            now,
            focus,
            0,
            voiced,
            a.mute,
            0,
            ...hexToRgb(o.light),
            0,
            ...hexToRgb(o.chlorophyll),
            o.chlorophyllThickness,
            ...hexToRgb(o.dye),
            o.dyeThickness,
          ]),
        );
        const group = groups[steam.current];
        const emitGroup = emitGroups[steam.current];
        if (!group || !emitGroup) return;
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
        steam.dispose();
        buffer.destroy();
        ctx.unconfigure();
      };
    });
    return () => {
      dead = true;
      cancelAnimationFrame(raf);
      cleanup();
    };
    // the shaders are deps so editing either .wgsl rebuilds on hot reload
  }, [orbShader, steamShader]);

  return (
    <span className="orb" style={{ width: size, height: size }} data-mode={mode}>
      <span
        className="orb-flat"
        aria-hidden="true"
        style={{
          opacity: gpu ? 0 : 1,
          // a still frame of her (public/rumi.png) until her shader is drawing, or without WebGPU
          background: "center / cover url(/rumi.png)",
          filter: muted ? "saturate(.92) brightness(.82)" : "none",
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

import { DESIGN } from "../design.ts";
import {
  BUFFER,
  compilePipeline,
  TEXTURE,
  canvasFormat,
  drawFullscreen,
  webgpuContext,
  hexToRgb,
  reducedMotion,
} from "./gpu.ts";
import { emissionTexture } from "../rumi/emission.ts";

// The phone's GPU scene, drawn every frame:
//   ink water -> offscreen texture -> background canvas
//   ink texture -> separable blur
//   glass shapes (refracting ink + blurred ink) -> overlay canvas above the page

// capsule: corners are true circles; otherwise they're continuous (squircle) corners.
export type GlassShape = {
  cx: number;
  cy: number;
  hw: number;
  hh: number;
  radius: number;
  capsule: boolean;
  shine: number; // 0..1: how much the glass reflects (interactive 1, static less)
};

export type SceneShaders = { ink: string; blur: string; blit: string; glass: string };

const MAX_RIPPLES = 8;
const MAX_SHAPES = 6;
const MAX_BLUR = 40;
/** How far behind Rumi the wall her light bounces off is (css px): it sets how widely that light spreads. */
export const SUN_REACH = 160;

/** The colour of Rumi's light before her first emission frame comes back: her backlight. */
export function sunLight(): [number, number, number] {
  return hexToRgb(DESIGN.orb.light);
}
const INK_FLOATS = 4 * (6 + MAX_RIPPLES);
const GLASS_FLOATS = 4 * (7 + 2 * MAX_SHAPES);

type Targets = {
  width: number;
  height: number;
  ink: GPUTexture;
  tmp: GPUTexture;
  blur: GPUTexture;
  groups: { blit: GPUBindGroup; hblur: GPUBindGroup; vblur: GPUBindGroup; glass: GPUBindGroup };
};

function gaussian(radius: number): Float32Array {
  const sigma = Math.max(radius / 3, 0.5);
  const w = new Float32Array(MAX_BLUR + 1);
  let sum = 0;
  for (let i = 0; i <= radius; i++) {
    const x = Math.exp(-0.5 * ((i * i) / (sigma * sigma)));
    w[i] = x;
    sum += i === 0 ? x : 2 * x;
  }
  return w.map((x) => x / sum);
}

type Pipelines = {
  ink: GPURenderPipeline;
  blit: GPURenderPipeline;
  blur: GPURenderPipeline;
  glass: GPURenderPipeline;
};

async function compileAll(device: GPUDevice, s: SceneShaders): Promise<Pipelines> {
  const format = canvasFormat();
  const [ink, blit, blur, glass] = await Promise.all([
    compilePipeline(device, s.ink, "rgba8unorm", "ink.wgsl"),
    compilePipeline(device, s.blit, format, "blit.wgsl"),
    compilePipeline(device, s.blur, "rgba8unorm", "blur.wgsl"),
    compilePipeline(device, s.glass, format, "glass.wgsl"),
  ]);
  return { ink, blit, blur, glass };
}

export class Scene {
  private readonly bg: GPUCanvasContext;
  private readonly glass: GPUCanvasContext;
  private readonly sampler: GPUSampler;
  private readonly ringSampler: GPUSampler;
  private readonly inkUniforms: GPUBuffer;
  private readonly glassUniforms: GPUBuffer;
  private readonly hblurUniforms: GPUBuffer;
  private readonly vblurUniforms: GPUBuffer;
  private readonly weights: GPUBuffer;
  private pipelines: Pipelines;
  private inkGroup: GPUBindGroup;
  private targets: Targets | null = null;
  private ripples: number[][] = [];
  private blurRadius = -1;
  private voice = { x: 0, y: 0, on: false, strength: 0, phase: 0, last: 0 };
  private readonly t0 = performance.now();

  static async create(
    device: GPUDevice,
    bgCanvas: HTMLCanvasElement,
    glassCanvas: HTMLCanvasElement,
    shaders: SceneShaders,
  ): Promise<Scene> {
    return new Scene(device, bgCanvas, glassCanvas, await compileAll(device, shaders));
  }

  private constructor(
    private readonly device: GPUDevice,
    private readonly bgCanvas: HTMLCanvasElement,
    private readonly glassCanvas: HTMLCanvasElement,
    pipelines: Pipelines,
  ) {
    const format = canvasFormat();
    this.bg = this.context(bgCanvas, format, "opaque");
    this.glass = this.context(glassCanvas, format, "premultiplied");
    this.ringSampler = device.createSampler({
      magFilter: "linear",
      minFilter: "linear",
      addressModeU: "repeat",
      addressModeV: "clamp-to-edge",
    });
    this.sampler = device.createSampler({
      magFilter: "linear",
      minFilter: "linear",
      addressModeU: "clamp-to-edge",
      addressModeV: "clamp-to-edge",
    });
    const uniform = (floats: number, label: string) =>
      device.createBuffer({ label, size: floats * 4, usage: BUFFER.UNIFORM | BUFFER.COPY_DST });
    this.inkUniforms = uniform(INK_FLOATS, "ink");
    this.glassUniforms = uniform(GLASS_FLOATS, "glass");
    this.hblurUniforms = uniform(4, "hblur");
    this.vblurUniforms = uniform(4, "vblur");
    this.weights = device.createBuffer({
      label: "blur weights",
      size: (MAX_BLUR + 1) * 4,
      usage: BUFFER.STORAGE | BUFFER.COPY_DST,
    });
    this.pipelines = pipelines;
    this.inkGroup = this.makeInkGroup();
  }

  private makeInkGroup(): GPUBindGroup {
    return this.device.createBindGroup({
      layout: this.pipelines.ink.getBindGroupLayout(0),
      entries: [
        { binding: 0, resource: { buffer: this.inkUniforms } },
        // Rumi's light toward the wall, by direction (orb.wgsl's fs_emission)
        { binding: 1, resource: emissionTexture(this.device).createView() },
        { binding: 2, resource: this.ringSampler },
      ],
    });
  }

  private context(
    canvas: HTMLCanvasElement,
    format: GPUTextureFormat,
    alphaMode: GPUCanvasAlphaMode,
  ): GPUCanvasContext {
    const ctx = webgpuContext(canvas);
    if (!ctx) throw new Error("no webgpu context");
    ctx.configure({ device: this.device, format, alphaMode });
    return ctx;
  }

  /**
   * Recompiles after a .wgsl edit. A shader that fails to compile leaves the
   * last good pipelines running and comes back as the error message.
   */
  async setShaders(s: SceneShaders): Promise<string | null> {
    try {
      this.pipelines = await compileAll(this.device, s);
    } catch (err) {
      return err instanceof Error ? err.message : String(err);
    }
    this.inkGroup = this.makeInkGroup();
    this.targets = null;
    return null;
  }

  now(): number {
    return (performance.now() - this.t0) / 1000;
  }

  /** A ripple in the ink at (x, y) css px from the phone's top left. */
  ripple(x: number, y: number, amp: number): void {
    this.ripples.push([x, y, this.now(), amp]);
    if (this.ripples.length > MAX_RIPPLES) this.ripples.shift();
  }

  /** Rumi's centre (css px) and whether she's speaking; her waves ease in and out. */
  setVoice(x: number, y: number, on: boolean): void {
    this.voice.x = x;
    this.voice.y = y;
    this.voice.on = on;
  }

  private ensureTargets(width: number, height: number): Targets {
    if (this.targets && this.targets.width === width && this.targets.height === height)
      return this.targets;
    this.targets?.ink.destroy();
    this.targets?.tmp.destroy();
    this.targets?.blur.destroy();
    const d = this.device;
    const tex = (label: string) =>
      d.createTexture({
        label,
        size: [width, height],
        format: "rgba8unorm",
        usage: TEXTURE.RENDER_ATTACHMENT | TEXTURE.TEXTURE_BINDING,
      });
    const ink = tex("ink");
    const tmp = tex("blur tmp");
    const blur = tex("blur");
    const p = this.pipelines;
    const blurGroup = (src: GPUTexture, buf: GPUBuffer) =>
      d.createBindGroup({
        layout: p.blur.getBindGroupLayout(0),
        entries: [
          { binding: 0, resource: { buffer: buf } },
          { binding: 1, resource: src.createView() },
          { binding: 2, resource: this.sampler },
          { binding: 3, resource: { buffer: this.weights } },
        ],
      });
    this.targets = {
      width,
      height,
      ink,
      tmp,
      blur,
      groups: {
        blit: d.createBindGroup({
          layout: p.blit.getBindGroupLayout(0),
          entries: [
            { binding: 0, resource: ink.createView() },
            { binding: 1, resource: this.sampler },
          ],
        }),
        hblur: blurGroup(ink, this.hblurUniforms),
        vblur: blurGroup(tmp, this.vblurUniforms),
        glass: d.createBindGroup({
          layout: p.glass.getBindGroupLayout(0),
          entries: [
            { binding: 0, resource: { buffer: this.glassUniforms } },
            { binding: 1, resource: blur.createView() },
            { binding: 2, resource: ink.createView() },
            { binding: 3, resource: this.sampler },
          ],
        }),
      },
    };
    return this.targets;
  }

  frame(shapes: readonly GlassShape[], cssWidth: number, cssHeight: number, dpr: number): void {
    const width = Math.max(1, Math.round(cssWidth * dpr));
    const height = Math.max(1, Math.round(cssHeight * dpr));
    for (const c of [this.bgCanvas, this.glassCanvas]) {
      if (c.width !== width || c.height !== height) {
        c.width = width;
        c.height = height;
      }
    }
    const tg = this.ensureTargets(width, height);
    const q = this.device.queue;
    const now = this.now();

    const ink = new Float32Array(INK_FLOATS);
    ink.set([width, height, now, dpr], 0);
    ink.set(
      [
        DESIGN.ink.rippleSpeed,
        reducedMotion() ? 0 : DESIGN.ink.swell,
        DESIGN.ink.gloss,
        DESIGN.ink.lifetime,
      ],
      4,
    );
    ink.set([...hexToRgb(DESIGN.ink.tint), 1], 8);
    const vo = this.voice;
    const dt = Math.min(0.1, Math.max(0, now - vo.last));
    vo.last = now;
    vo.strength +=
      ((vo.on && !reducedMotion() ? 1 : 0) - vo.strength) * Math.min(1, dt * DESIGN.ink.voiceEase);
    vo.phase +=
      (dt * Math.PI * 2 * DESIGN.ink.voiceSpeed) / Math.max(DESIGN.ink.voiceWavelength, 1);
    ink.set([vo.x, vo.y, vo.strength, vo.phase], 12);
    ink.set([DESIGN.ink.voiceWavelength, DESIGN.ink.voiceAmp, DESIGN.ink.voiceReach, 0], 16);
    ink.set([1, 0, 0, SUN_REACH], 20);
    this.ripples.forEach((r, i) => ink.set(r, 24 + i * 4));
    q.writeBuffer(this.inkUniforms, 0, ink);

    const radius = Math.round(Math.min(MAX_BLUR, Math.max(1, DESIGN.glass.blurRadius * dpr)));
    if (radius !== this.blurRadius) {
      this.blurRadius = radius;
      q.writeBuffer(this.weights, 0, gaussian(radius));
    }
    q.writeBuffer(this.hblurUniforms, 0, new Float32Array([1 / width, 0, radius, 0]));
    q.writeBuffer(this.vblurUniforms, 0, new Float32Array([0, 1 / height, radius, 0]));

    const g = DESIGN.glass;
    const glass = new Float32Array(GLASS_FLOATS);
    const count = Math.min(shapes.length, MAX_SHAPES);
    glass.set([width, height, dpr, count], 0);
    glass.set([...hexToRgb(g.tint), g.tintAlpha], 4);
    glass.set([g.refThickness, g.refFactor, g.refDispersion, g.refDistance], 8);
    glass.set([g.refFresnelRange, g.refFresnelHardness / 100, g.refFresnelFactor / 100, 0], 12);
    glass.set(
      [g.glareRange, g.glareHardness / 100, g.glareConvergence / 100, g.glareOppositeFactor / 100],
      16,
    );
    glass.set(
      [g.glareFactor / 100, (g.glareAngle * Math.PI) / 180, g.mergeRate, g.blurEdge ? 1 : 0],
      20,
    );
    glass.set([g.shadowExpand, g.shadowFactor / 100, g.cover, 0], 24);
    shapes
      .slice(0, count)
      .forEach((s, i) =>
        glass.set(
          [s.cx, s.cy, s.hw, s.hh, s.radius, s.capsule ? 2 : g.roundness, s.shine, 0],
          28 + i * 8,
        ),
      );
    q.writeBuffer(this.glassUniforms, 0, glass);

    const enc = this.device.createCommandEncoder();
    const p = this.pipelines;
    drawFullscreen(enc, tg.ink.createView(), p.ink, this.inkGroup);
    drawFullscreen(enc, this.bg.getCurrentTexture().createView(), p.blit, tg.groups.blit);
    drawFullscreen(enc, tg.tmp.createView(), p.blur, tg.groups.hblur);
    drawFullscreen(enc, tg.blur.createView(), p.blur, tg.groups.vblur);
    drawFullscreen(enc, this.glass.getCurrentTexture().createView(), p.glass, tg.groups.glass);
    q.submit([enc.finish()]);
  }

  dispose(): void {
    this.targets?.ink.destroy();
    this.targets?.tmp.destroy();
    this.targets?.blur.destroy();
    for (const b of [
      this.inkUniforms,
      this.glassUniforms,
      this.hblurUniforms,
      this.vblurUniforms,
      this.weights,
    ])
      b.destroy();
    this.bg.unconfigure();
    this.glass.unconfigure();
  }
}

import { BUFFER, TEXTURE, compilePipeline, drawFullscreen } from "../gpu/gpu.ts";

// Rumi's steam: green and teal gases in slowly moving air, simulated as an
// incompressible fluid on a small wrap-around grid (steam.wgsl has the
// physics). Orb.tsx steps it once a frame and samples its gas texture.

const N = 96;
// pressure relaxation passes per step: more is stiffer, truer incompressibility
const JACOBI = 16;
// seconds of simulation run up front, so she never starts empty, in steps of
// WARM_STEP seconds (the advection stays stable at large steps). Each step is
// its own submission: one huge one makes Firefox reset the GPU device.
const WARM_UP = 6;
const WARM_STEP = 0.5;

export type SteamParams = {
  /** wind (grid uv/s) */
  wind: [number, number];
  /** how quickly the air settles to the wind (1/s) */
  settle: number;
  /** how strongly the gas rises */
  buoyancy: number;
  /** gas diffusion and fading (1/s) */
  diffusion: number;
  fading: number;
  /** stirring swirl strength */
  swirl: number;
  /** gas fed per second by each source (thickness/s) */
  feed: number;
};

type Pass = "fs_velocity" | "fs_divergence" | "fs_jacobi" | "fs_project" | "fs_gas";
const PASSES: readonly Pass[] = [
  "fs_velocity",
  "fs_divergence",
  "fs_jacobi",
  "fs_project",
  "fs_gas",
];

export class Steam {
  private readonly uniforms: GPUBuffer;
  private readonly sampler: GPUSampler;
  private readonly vel: [GPUTexture, GPUTexture];
  private readonly pressure: [GPUTexture, GPUTexture];
  private readonly divergence: GPUTexture;
  private readonly gasTex: [GPUTexture, GPUTexture];
  /** a view of each gas texture; `current` says which holds the latest step */
  readonly gas: [GPUTextureView, GPUTextureView];
  current = 0;
  private time = 0;

  static async create(device: GPUDevice, shader: string, params: SteamParams): Promise<Steam> {
    // one explicit layout for every pass (an automatic one would drop the
    // bindings a pass happens not to read)
    const fragment = 0x2; // GPUShaderStage.FRAGMENT
    const bindings = device.createBindGroupLayout({
      label: "steam",
      entries: [
        { binding: 0, visibility: fragment, buffer: { type: "uniform" } },
        { binding: 1, visibility: fragment, texture: { sampleType: "float" } },
        { binding: 2, visibility: fragment, texture: { sampleType: "float" } },
        { binding: 3, visibility: fragment, sampler: { type: "filtering" } },
      ],
    });
    const layout = device.createPipelineLayout({ bindGroupLayouts: [bindings] });
    const entries = await Promise.all(
      PASSES.map((p) => compilePipeline(device, shader, "rgba16float", "steam.wgsl", p, layout)),
    );
    const pipelines = Object.fromEntries(PASSES.map((p, i) => [p, entries[i]])) as Record<
      Pass,
      GPURenderPipeline
    >;
    const steam = new Steam(device, pipelines, bindings);
    for (let t = 0; t < WARM_UP; t += WARM_STEP) {
      const enc = device.createCommandEncoder();
      steam.step(enc, WARM_STEP, params);
      device.queue.submit([enc.finish()]);
    }
    return steam;
  }

  private constructor(
    private readonly device: GPUDevice,
    private readonly pipelines: Record<Pass, GPURenderPipeline>,
    private readonly bindings: GPUBindGroupLayout,
  ) {
    const tex = (label: string) =>
      device.createTexture({
        label,
        size: [N, N],
        format: "rgba16float",
        usage: TEXTURE.RENDER_ATTACHMENT | TEXTURE.TEXTURE_BINDING,
      });
    this.vel = [tex("steam velocity a"), tex("steam velocity b")];
    this.pressure = [tex("steam pressure a"), tex("steam pressure b")];
    this.divergence = tex("steam divergence");
    this.gasTex = [tex("steam gas a"), tex("steam gas b")];
    this.gas = [this.gasTex[0].createView(), this.gasTex[1].createView()];
    this.uniforms = device.createBuffer({
      label: "steam",
      size: 7 * 16,
      usage: BUFFER.UNIFORM | BUFFER.COPY_DST,
    });
    this.sampler = device.createSampler({
      magFilter: "linear",
      minFilter: "linear",
      addressModeU: "repeat",
      addressModeV: "repeat",
    });
  }

  private pass(enc: GPUCommandEncoder, p: Pass, out: GPUTexture, a: GPUTexture, b: GPUTexture) {
    const pipeline = this.pipelines[p];
    const group = this.device.createBindGroup({
      layout: this.bindings,
      entries: [
        { binding: 0, resource: { buffer: this.uniforms } },
        { binding: 1, resource: a.createView() },
        { binding: 2, resource: b.createView() },
        { binding: 3, resource: this.sampler },
      ],
    });
    drawFullscreen(enc, out.createView(), pipeline, group);
  }

  /** Advances the steam by dt seconds, encoding its passes into enc. */
  step(enc: GPUCommandEncoder, dt: number, p: SteamParams): void {
    if (dt <= 0) return;
    this.time += dt;
    const t = this.time;
    // four sources drifting slowly around the grid, two of each gas
    const source = (k: number, r: number) => [
      0.5 + 0.38 * Math.sin(t * (0.031 + 0.007 * k) + k * 1.9),
      0.5 + 0.38 * Math.cos(t * (0.027 + 0.006 * k) + k * 2.7),
      r,
      p.feed,
    ];
    // (queue.writeBuffer lands before this encoder's passes run, so call step
    // at most once per submission)
    this.device.queue.writeBuffer(
      this.uniforms,
      0,
      new Float32Array([
        1 / N,
        1 / N,
        dt,
        t,
        ...p.wind,
        p.settle,
        p.buoyancy,
        p.diffusion,
        p.fading,
        p.swirl,
        0,
        ...source(0, 0.11),
        ...source(1, 0.09),
        ...source(2, 0.08),
        ...source(3, 0.1),
      ]),
    );
    const [v0, v1] = this.vel;
    const [g0, g1] = this.gasTex;
    const [gasNow, gasNext] = this.current === 0 ? [g0, g1] : [g1, g0];
    this.pass(enc, "fs_velocity", v1, v0, gasNow);
    this.pass(enc, "fs_divergence", this.divergence, v1, v1);
    let [p0, p1] = this.pressure;
    for (let i = 0; i < JACOBI; i++) {
      this.pass(enc, "fs_jacobi", p1, p0, this.divergence);
      [p0, p1] = [p1, p0];
    }
    this.pass(enc, "fs_project", v0, v1, p0);
    this.pass(enc, "fs_gas", gasNext, gasNow, v0);
    this.current = 1 - this.current;
  }

  dispose(): void {
    for (const t of [...this.vel, ...this.pressure, this.divergence, ...this.gasTex]) t.destroy();
    this.uniforms.destroy();
  }
}

import lgsColor from "../shaders/lgs-color.wgsl?raw";

// One GPUDevice for the whole page: the ink, the glass and every orb share it.

let devicePromise: Promise<GPUDevice | null> | null = null;

export function getDevice(): Promise<GPUDevice | null> {
  devicePromise ??= (async () => {
    if (!("gpu" in navigator)) return null;
    const adapter = await navigator.gpu.requestAdapter();
    if (!adapter) return null;
    const device = await adapter.requestDevice();
    void device.lost.then((info) => {
      console.warn(`WebGPU device lost (${info.reason}): ${info.message}`);
      devicePromise = null;
    });
    device.addEventListener("uncapturederror", (e) => {
      console.error(`WebGPU: ${(e as GPUUncapturedErrorEvent).error.message}`);
    });
    return device;
  })().catch(() => null);
  return devicePromise;
}

// TypeScript's DOM lib types WebGPU objects but not these flag namespaces; values per the spec.
export const BUFFER = {
  MAP_READ: 0x0001,
  COPY_DST: 0x0008,
  UNIFORM: 0x0040,
  STORAGE: 0x0080,
} as const;
export const TEXTURE = { COPY_SRC: 0x01, TEXTURE_BINDING: 0x04, RENDER_ATTACHMENT: 0x10 } as const;
export const MAP_MODE_READ = 0x0001;

export function webgpuContext(canvas: HTMLCanvasElement): GPUCanvasContext | null {
  const ctx = canvas.getContext("webgpu");
  return ctx instanceof GPUCanvasContext ? ctx : null;
}

export const canvasFormat = (): GPUTextureFormat => navigator.gpu.getPreferredCanvasFormat();

// Fullscreen triangle; uv is (0,0) at the top left.
const VERTEX = /* wgsl */ `
struct VOut {
  @builtin(position) position: vec4f,
  @location(0) uv: vec2f,
}

@vertex
fn vs_main(@builtin(vertex_index) i: u32) -> VOut {
  let xy = array<vec2f, 3>(vec2f(-1.0, -1.0), vec2f(3.0, -1.0), vec2f(-1.0, 3.0))[i];
  var out: VOut;
  out.position = vec4f(xy, 0.0, 1.0);
  out.uv = vec2f(xy.x * 0.5 + 0.5, 0.5 - xy.y * 0.5);
  return out;
}
`;

const INCLUDES = { "lgs-color": lgsColor } as const;

const isInclude = (name: string): name is keyof typeof INCLUDES => name in INCLUDES;

// Expand includes and prepend the vertex stage, remembering where every line
// came from so compile errors point at the .wgsl file you're editing.
function assemble(fragment: string, file: string): { code: string; origin: string[] } {
  const lines: string[] = [];
  const origin: string[] = [];
  VERTEX.split("\n").forEach((l, i) => {
    lines.push(l);
    origin.push(`vertex:${i + 1}`);
  });
  fragment.split("\n").forEach((l, i) => {
    const inc = /^#include ([\w-]+)$/.exec(l)?.[1];
    if (inc === undefined) {
      lines.push(l);
      origin.push(`${file}:${i + 1}`);
      return;
    }
    if (!isInclude(inc)) throw new Error(`${file}:${i + 1}: unknown include ${inc}`);
    INCLUDES[inc].split("\n").forEach((il, j) => {
      lines.push(il);
      origin.push(`${inc}.wgsl:${j + 1}`);
    });
  });
  return { code: lines.join("\n"), origin };
}

/**
 * Compiles a fullscreen pipeline, rejecting with a readable WGSL error so a
 * broken edit can be reported while the last good pipeline keeps running.
 */
export async function compilePipeline(
  device: GPUDevice,
  fragment: string,
  format: GPUTextureFormat,
  file: string,
  entryPoint = "fs_main",
  layout: GPUPipelineLayout | "auto" = "auto",
): Promise<GPURenderPipeline> {
  const { code, origin } = assemble(fragment, file);
  const module = device.createShaderModule({ label: file, code });
  const info = await module.getCompilationInfo();
  const errors = info.messages.filter((m) => m.type === "error");
  if (errors.length > 0) {
    throw new Error(errors.map((m) => `${origin[m.lineNum - 1] ?? file}: ${m.message}`).join("\n"));
  }
  return device.createRenderPipelineAsync({
    label: file,
    layout,
    vertex: { module, entryPoint: "vs_main" },
    fragment: { module, entryPoint, targets: [{ format }] },
    primitive: { topology: "triangle-list" },
  });
}

export function drawFullscreen(
  encoder: GPUCommandEncoder,
  view: GPUTextureView,
  pipeline: GPURenderPipeline,
  bindGroup: GPUBindGroup,
): void {
  const pass = encoder.beginRenderPass({
    colorAttachments: [{ view, loadOp: "clear", storeOp: "store", clearValue: [0, 0, 0, 0] }],
  });
  pass.setPipeline(pipeline);
  pass.setBindGroup(0, bindGroup);
  pass.draw(3);
  pass.end();
}

export const hexToRgb = (hex: string): [number, number, number] => [
  parseInt(hex.slice(1, 3), 16) / 255,
  parseInt(hex.slice(3, 5), 16) / 255,
  parseInt(hex.slice(5, 7), 16) / 255,
];

export const reducedMotion = (): boolean =>
  typeof matchMedia !== "undefined" && matchMedia("(prefers-reduced-motion: reduce)").matches;

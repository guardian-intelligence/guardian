import { WakeScheduler } from "./scheduler";

export type IlluminationMode = "css" | "webgl2";

export interface IlluminationRenderer {
  readonly mode: IlluminationMode;
  dispose(): void;
}

type ParticleLayer = {
  readonly alpha: readonly [number, number];
  readonly blur: number;
  readonly density: number;
  readonly size: readonly [number, number];
  readonly speed: readonly [number, number];
};

const PARTICLE_LAYERS: readonly ParticleLayer[] = [
  {
    alpha: [0.1, 0.28],
    blur: 0.4,
    density: 0.0002,
    size: [0.3, 0.55],
    speed: [0.002, 0.005],
  },
  {
    alpha: [0.15, 0.4],
    blur: 0.8,
    density: 0.000065,
    size: [0.45, 0.8],
    speed: [0.006, 0.011],
  },
  {
    alpha: [0.2, 0.55],
    blur: 1.8,
    density: 0.000018,
    size: [0.7, 1.15],
    speed: [0.012, 0.02],
  },
] as const;

const FLOATS_PER_PARTICLE = 8;

const contextOptions: WebGLContextAttributes = {
  alpha: true,
  antialias: false,
  depth: false,
  failIfMajorPerformanceCaveat: true,
  powerPreference: "high-performance",
  premultipliedAlpha: true,
  preserveDrawingBuffer: false,
  stencil: false,
};

const particleVertexShaderSource = `#version 300 es
precision highp float;

layout(location = 0) in vec4 aPositionSizeSpeed;
layout(location = 1) in vec4 aAppearance;

uniform float uPixelRatio;
uniform float uTime;

out float vAlpha;
out float vBlur;
out float vCoreRadius;

void main() {
  float speed = aPositionSizeSpeed.w;
  vec2 position = vec2(
    mod(aPositionSizeSpeed.x + 0.02 - speed * uTime * 0.08, 1.04) - 0.02,
    mod(aPositionSizeSpeed.y + 0.02 - speed * uTime, 1.04) - 0.02
  );
  vec2 clip = position * 2.0 - 1.0;
  gl_Position = vec4(clip.x, -clip.y, 0.0, 1.0);

  float radius = aPositionSizeSpeed.z;
  float blur = aAppearance.w;
  float renderedRadius = radius + blur * 0.5;
  gl_PointSize = max(1.0, renderedRadius * 2.0 * uPixelRatio);
  vCoreRadius = radius / max(renderedRadius, 0.001);
  vBlur = blur;
  vAlpha = aAppearance.x * (
    0.58 + 0.42 * ((sin(uTime * aAppearance.z + aAppearance.y) + 1.0) * 0.5)
  );
}
`;

const particleFragmentShaderSource = `#version 300 es
precision highp float;

in float vAlpha;
in float vBlur;
in float vCoreRadius;

out vec4 outColor;

void main() {
  vec2 point = gl_PointCoord * 2.0 - 1.0;
  float distanceFromCenter = length(point);
  if (distanceFromCenter > 1.0) discard;

  float core = 1.0 - smoothstep(max(0.05, vCoreRadius * 0.55), vCoreRadius, distanceFromCenter);
  float haloFalloff = mix(8.0, 3.25, clamp(vBlur / 1.8, 0.0, 1.0));
  float halo = exp(-distanceFromCenter * distanceFromCenter * haloFalloff) * (1.0 - core);
  float alpha = vAlpha * (core + halo * 0.42);
  vec3 coreColor = vec3(216.0, 236.0, 248.0) / 255.0;
  vec3 haloColor = vec3(174.0, 207.0, 242.0) / 255.0;
  vec3 color = mix(haloColor, coreColor, core);
  outColor = vec4(color * alpha, alpha);
}
`;

const lightVertexShaderSource = `#version 300 es
precision highp float;

const vec2 POSITIONS[3] = vec2[3](
  vec2(-1.0, -1.0),
  vec2(3.0, -1.0),
  vec2(-1.0, 3.0)
);

out vec2 vUv;

void main() {
  vec2 position = POSITIONS[gl_VertexID];
  vUv = position * 0.5 + 0.5;
  gl_Position = vec4(position, 0.0, 1.0);
}
`;

const lightFragmentShaderSource = `#version 300 es
precision highp float;

uniform vec4 uPointerLight;
uniform vec2 uCssResolution;

in vec2 vUv;
out vec4 outColor;

void main() {
  vec2 point = vec2(vUv.x, 1.0 - vUv.y) * uCssResolution;
  float normalizedDistance = distance(point, uPointerLight.xy) / uPointerLight.z;
  float influence = pow(max(0.0, 1.0 - normalizedDistance), 2.6) * uPointerLight.w;
  vec3 color = vec3(0.38, 0.70, 1.0);
  outColor = vec4(color * influence, influence);
}
`;

type ParallelShaderCompile = {
  readonly COMPLETION_STATUS_KHR: number;
};

function between([minimum, maximum]: readonly [number, number]) {
  return Math.random() * (maximum - minimum) + minimum;
}

function nextAnimationFrame() {
  return new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()));
}

function compileShader(context: WebGL2RenderingContext, type: number, source: string): WebGLShader {
  const shader = context.createShader(type);
  if (!shader) throw new Error("Could not create illumination shader");
  context.shaderSource(shader, source);
  context.compileShader(shader);
  return shader;
}

async function createProgram(
  context: WebGL2RenderingContext,
  vertexSource: string,
  fragmentSource: string,
): Promise<WebGLProgram> {
  const vertexShader = compileShader(context, context.VERTEX_SHADER, vertexSource);
  const fragmentShader = compileShader(context, context.FRAGMENT_SHADER, fragmentSource);
  const program = context.createProgram();
  if (!program) throw new Error("Could not create illumination program");

  context.attachShader(program, vertexShader);
  context.attachShader(program, fragmentShader);
  context.linkProgram(program);

  const parallelCompile = context.getExtension(
    "KHR_parallel_shader_compile",
  ) as ParallelShaderCompile | null;
  if (parallelCompile) {
    while (!context.getProgramParameter(program, parallelCompile.COMPLETION_STATUS_KHR)) {
      await nextAnimationFrame();
    }
  }

  for (const shader of [vertexShader, fragmentShader]) {
    if (!context.getShaderParameter(shader, context.COMPILE_STATUS)) {
      const message = context.getShaderInfoLog(shader) ?? "Unknown shader compilation error";
      context.deleteProgram(program);
      context.deleteShader(vertexShader);
      context.deleteShader(fragmentShader);
      throw new Error(message);
    }
  }

  if (!context.getProgramParameter(program, context.LINK_STATUS)) {
    const message = context.getProgramInfoLog(program) ?? "Unknown shader link error";
    context.deleteProgram(program);
    context.deleteShader(vertexShader);
    context.deleteShader(fragmentShader);
    throw new Error(message);
  }

  context.detachShader(program, vertexShader);
  context.detachShader(program, fragmentShader);
  context.deleteShader(vertexShader);
  context.deleteShader(fragmentShader);
  return program;
}

function requireUniform(
  context: WebGL2RenderingContext,
  program: WebGLProgram,
  name: string,
): WebGLUniformLocation {
  const location = context.getUniformLocation(program, name);
  if (!location) throw new Error(`Missing illumination uniform: ${name}`);
  return location;
}

class WebGLIlluminationRenderer implements IlluminationRenderer {
  readonly mode = "webgl2";
  readonly #canvas: HTMLCanvasElement;
  readonly #context: WebGL2RenderingContext;
  readonly #lightProgram: WebGLProgram;
  readonly #particleBuffer: WebGLBuffer;
  readonly #particleProgram: WebGLProgram;
  readonly #particleVertexArray: WebGLVertexArrayObject;
  readonly #reducedMotion: MediaQueryList;
  readonly #resizeObserver: ResizeObserver;
  readonly #scheduler: WakeScheduler;
  #frameCount = 0;
  #height = 1;
  #particleCount = 0;
  #pixelRatio = 1;
  #pointer: { x: number; y: number } | null = null;
  #startTime: number | null = null;
  #width = 1;

  constructor(
    canvas: HTMLCanvasElement,
    context: WebGL2RenderingContext,
    lightProgram: WebGLProgram,
    particleProgram: WebGLProgram,
  ) {
    this.#canvas = canvas;
    this.#context = context;
    this.#lightProgram = lightProgram;
    this.#particleProgram = particleProgram;

    const particleBuffer = context.createBuffer();
    const particleVertexArray = context.createVertexArray();
    if (!particleBuffer || !particleVertexArray) {
      throw new Error("Could not allocate illumination particle buffers");
    }
    this.#particleBuffer = particleBuffer;
    this.#particleVertexArray = particleVertexArray;

    context.disable(context.DEPTH_TEST);
    context.disable(context.STENCIL_TEST);
    context.enable(context.BLEND);
    context.blendEquation(context.FUNC_ADD);
    context.blendFunc(context.ONE, context.ONE);
    context.clearColor(0, 0, 0, 0);

    context.bindVertexArray(this.#particleVertexArray);
    context.bindBuffer(context.ARRAY_BUFFER, this.#particleBuffer);
    context.enableVertexAttribArray(0);
    context.vertexAttribPointer(
      0,
      4,
      context.FLOAT,
      false,
      FLOATS_PER_PARTICLE * Float32Array.BYTES_PER_ELEMENT,
      0,
    );
    context.enableVertexAttribArray(1);
    context.vertexAttribPointer(
      1,
      4,
      context.FLOAT,
      false,
      FLOATS_PER_PARTICLE * Float32Array.BYTES_PER_ELEMENT,
      4 * Float32Array.BYTES_PER_ELEMENT,
    );
    context.bindVertexArray(null);

    this.#reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
    this.#scheduler = new WakeScheduler(
      () => this.#drawFrame(),
      (state) => {
        this.#canvas.dataset.state = state;
      },
    );
    this.#resizeObserver = new ResizeObserver(() => this.#resize());
    this.#resizeObserver.observe(canvas);
    window.addEventListener("pointermove", this.#onPointerMove, { passive: true });
    document.documentElement.addEventListener("pointerleave", this.#onPointerLeave, {
      passive: true,
    });
    document.addEventListener("visibilitychange", this.#onVisibilityChange);
    this.#reducedMotion.addEventListener("change", this.#onReducedMotionChange);
    this.#resize();
  }

  readonly #onPointerMove = (event: PointerEvent) => {
    if (this.#reducedMotion.matches || document.visibilityState === "hidden") return;
    this.#pointer = { x: event.clientX, y: event.clientY };
    this.#scheduler.wake();
  };

  readonly #onPointerLeave = () => {
    this.#pointer = null;
  };

  readonly #onReducedMotionChange = () => {
    this.#pointer = null;
    this.#startTime = null;
    if (this.#reducedMotion.matches) {
      this.#scheduler.dispose();
      this.#drawFrame();
      return;
    }
    this.#scheduler.wake();
  };

  readonly #onVisibilityChange = () => {
    if (document.visibilityState === "hidden") {
      this.#scheduler.suspend();
      return;
    }
    this.#startTime = null;
    this.#scheduler.resume();
    this.#scheduler.wake();
  };

  #makeParticleData(width: number, height: number) {
    const particleCount = PARTICLE_LAYERS.reduce(
      (total, layer) => total + Math.max(1, Math.round(width * height * layer.density)),
      0,
    );
    const data = new Float32Array(particleCount * FLOATS_PER_PARTICLE);
    let offset = 0;

    for (const layer of PARTICLE_LAYERS) {
      const count = Math.max(1, Math.round(width * height * layer.density));
      for (let index = 0; index < count; index += 1) {
        data.set(
          [
            Math.random(),
            Math.random(),
            between(layer.size),
            between(layer.speed),
            between(layer.alpha),
            Math.random() * Math.PI * 2,
            between([0.35, 0.8]),
            layer.blur,
          ],
          offset,
        );
        offset += FLOATS_PER_PARTICLE;
      }
    }

    this.#particleCount = particleCount;
    return data;
  }

  #resize() {
    const { height, width } = this.#canvas.getBoundingClientRect();
    const pixelRatio = Math.min(window.devicePixelRatio || 1, 2);
    const backingWidth = Math.max(1, Math.round(width * pixelRatio));
    const backingHeight = Math.max(1, Math.round(height * pixelRatio));
    if (this.#canvas.width === backingWidth && this.#canvas.height === backingHeight) return;

    this.#height = height;
    this.#pixelRatio = pixelRatio;
    this.#width = width;
    this.#canvas.width = backingWidth;
    this.#canvas.height = backingHeight;
    this.#context.viewport(0, 0, backingWidth, backingHeight);
    this.#context.bindBuffer(this.#context.ARRAY_BUFFER, this.#particleBuffer);
    this.#context.bufferData(
      this.#context.ARRAY_BUFFER,
      this.#makeParticleData(width, height),
      this.#context.STATIC_DRAW,
    );
    this.#startTime = null;
    this.#drawFrame();
    if (!this.#reducedMotion.matches) this.#scheduler.wake();
  }

  #drawFrame() {
    const context = this.#context;
    if (context.isContextLost()) return false;
    const now = performance.now();
    this.#startTime ??= now;
    const elapsedSeconds = this.#reducedMotion.matches ? 0 : (now - this.#startTime) / 1000;

    context.clear(context.COLOR_BUFFER_BIT);

    if (this.#pointer) {
      context.useProgram(this.#lightProgram);
      context.uniform2f(
        requireUniform(context, this.#lightProgram, "uCssResolution"),
        this.#width,
        this.#height,
      );
      context.uniform4f(
        requireUniform(context, this.#lightProgram, "uPointerLight"),
        this.#pointer.x,
        this.#pointer.y,
        190,
        0.055,
      );
      context.drawArrays(context.TRIANGLES, 0, 3);
    }

    context.useProgram(this.#particleProgram);
    context.uniform1f(
      requireUniform(context, this.#particleProgram, "uPixelRatio"),
      this.#pixelRatio,
    );
    context.uniform1f(requireUniform(context, this.#particleProgram, "uTime"), elapsedSeconds);
    context.bindVertexArray(this.#particleVertexArray);
    context.drawArrays(context.POINTS, 0, this.#particleCount);
    context.bindVertexArray(null);

    this.#frameCount += 1;
    this.#canvas.dataset.frameCount = String(this.#frameCount);
    return !this.#reducedMotion.matches && document.visibilityState !== "hidden";
  }

  dispose() {
    this.#scheduler.dispose();
    window.removeEventListener("pointermove", this.#onPointerMove);
    document.documentElement.removeEventListener("pointerleave", this.#onPointerLeave);
    document.removeEventListener("visibilitychange", this.#onVisibilityChange);
    this.#reducedMotion.removeEventListener("change", this.#onReducedMotionChange);
    this.#resizeObserver.disconnect();
    this.#context.deleteBuffer(this.#particleBuffer);
    this.#context.deleteVertexArray(this.#particleVertexArray);
    this.#context.deleteProgram(this.#lightProgram);
    this.#context.deleteProgram(this.#particleProgram);
  }
}

const cssRenderer: IlluminationRenderer = {
  mode: "css",
  dispose() {},
};

export async function createIlluminationRenderer(
  canvas: HTMLCanvasElement,
): Promise<IlluminationRenderer> {
  const context = canvas.getContext("webgl2", contextOptions);
  if (!context) return cssRenderer;

  try {
    const [lightProgram, particleProgram] = await Promise.all([
      createProgram(context, lightVertexShaderSource, lightFragmentShaderSource),
      createProgram(context, particleVertexShaderSource, particleFragmentShaderSource),
    ]);
    return new WebGLIlluminationRenderer(canvas, context, lightProgram, particleProgram);
  } catch {
    return cssRenderer;
  }
}

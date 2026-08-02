import {
  canRenderHtmlInCanvas,
  type ElementImageContext,
  type PaintableCanvas,
} from "./html-in-canvas";
import { WakeScheduler } from "./scheduler";

export type CanvasMode = "canvas-ui" | "css" | "webgl2";

export interface CanvasRenderer {
  readonly mode: CanvasMode;
  dispose(): void;
}

export interface CanvasElements {
  readonly content: HTMLElement;
  readonly output: HTMLCanvasElement;
  readonly source: HTMLCanvasElement;
}

type ParticleLayer = {
  readonly alpha: readonly [number, number];
  readonly blur: number;
  readonly density: number;
  readonly size: readonly [number, number];
  readonly speed: readonly [number, number];
};

type Surface = {
  readonly element: HTMLElement;
  readonly kind: number;
  readonly opacity: number;
  readonly radius: number;
  readonly rect: readonly [number, number, number, number];
  activity: number;
};

type SceneUniforms = Record<
  "uIntro" | "uPixelRatio" | "uPointer" | "uResolution" | "uTime",
  WebGLUniformLocation
>;
type ParticleUniforms = Record<"uIntro" | "uPixelRatio" | "uTime", WebGLUniformLocation>;
type SurfaceUniforms = Record<
  "uContent" | "uHasContent" | "uPixelRatio" | "uResolution" | "uTime",
  WebGLUniformLocation
>;

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
const FLOATS_PER_SURFACE = 8;
const MAX_PIXEL_RATIO = 2;

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

const fullscreenVertexShader = `#version 300 es
precision highp float;

const vec2 POSITIONS[3] = vec2[3](
  vec2(-1.0, -1.0),
  vec2(3.0, -1.0),
  vec2(-1.0, 3.0)
);

void main() {
  gl_Position = vec4(POSITIONS[gl_VertexID], 0.0, 1.0);
}
`;

const sceneFragmentShader = `#version 300 es
precision highp float;

uniform vec2 uResolution;
uniform float uPixelRatio;
uniform float uTime;
uniform float uIntro;
uniform vec4 uPointer;

out vec4 outColor;

float hash(vec2 p) {
  vec3 p3 = fract(vec3(p.xyx) * 0.1031);
  p3 += dot(p3, p3.yzx + 33.33);
  return fract((p3.x + p3.y) * p3.z);
}

float line(float value, float position, float width) {
  return 1.0 - smoothstep(width * 0.35, width, abs(value - position));
}

float radial(vec2 point, vec2 center, vec2 scale) {
  vec2 delta = (point - center) / scale;
  return exp(-dot(delta, delta) * 2.1);
}

float beam(
  vec2 point,
  vec2 origin,
  float angle,
  float spread,
  float phase,
  float period
) {
  float motion = sin(uTime * 6.2831853 / period + phase) * 0.061;
  vec2 direction = normalize(vec2(sin(angle + motion), cos(angle + motion)));
  vec2 delta = point - origin;
  float along = dot(delta, direction);
  float across = abs(direction.x * delta.y - direction.y * delta.x);
  float width = 12.0 + max(along, 0.0) * spread;
  float core = exp(-pow(across / max(width, 1.0), 2.0) * 2.0);
  float enter = smoothstep(-12.0, 80.0, along);
  float leave = 1.0 - smoothstep(uResolution.y * 0.54, uResolution.y * 1.02, along);
  float pulse = 0.82 + 0.18 * sin(uTime * 6.2831853 / (period * 0.72) + phase * 2.0);
  return core * enter * leave * pulse;
}

void main() {
  vec2 point = vec2(gl_FragCoord.x, uResolution.y * uPixelRatio - gl_FragCoord.y) /
    uPixelRatio;
  vec2 center = vec2(uResolution.x * 0.5, uResolution.y * 0.36);
  float intro = clamp(uIntro, 0.0, 1.0);

  vec3 color = vec3(5.0, 6.0, 15.0) / 255.0;
  color += vec3(21.0, 61.0, 204.0) / 255.0 *
    radial(point, center, vec2(uResolution.x * 0.50, uResolution.y * 0.64)) * 0.083 * intro;
  color += vec3(152.0, 192.0, 239.0) / 255.0 *
    radial(point, vec2(uResolution.x * 0.5, uResolution.y * 0.32),
      vec2(uResolution.x * 0.43, uResolution.y * 0.60)) * 0.033 * intro;

  vec2 origin = vec2(uResolution.x * 0.5, min(100.0, uResolution.y * 0.13));
  float beams =
    beam(point, origin, radians(20.0), 0.17, 0.0, 8.5) * 0.19 +
    beam(point, origin, 0.0, 0.20, 2.1, 13.6) * 0.24 +
    beam(point, origin, radians(-20.0), 0.17, 4.2, 6.8) * 0.19;
  color += vec3(124.0, 145.0, 182.0) / 255.0 * beams * intro;

  float lineFade =
    (1.0 - smoothstep(uResolution.y * 0.68, uResolution.y, point.y)) * intro;
  float grid = line(point.x, uResolution.x * 0.5, 0.8) * 0.34;
  if (uResolution.x > 640.0) {
    grid += line(point.x, uResolution.x * 0.5 - 520.0, 0.8) * 0.40;
    grid += line(point.x, uResolution.x * 0.5 - 416.0, 0.8) * 0.72;
    grid += line(point.x, uResolution.x * 0.5 + 416.0, 0.8) * 0.72;
    grid += line(point.x, uResolution.x * 0.5 + 520.0, 0.8) * 0.40;
  }
  float headerY = uResolution.x <= 640.0 ? 85.0 : 105.0;
  float horizontalFade = smoothstep(0.0, uResolution.x * 0.22, point.x) *
    smoothstep(uResolution.x, uResolution.x * 0.78, point.x);
  grid += line(point.y, headerY, 0.8) * horizontalFade * 0.80;
  color += vec3(186.0, 215.0, 247.0) / 255.0 * grid * 0.052 * lineFade;

  if (uPointer.w > 0.001) {
    float distanceToPointer = distance(point, uPointer.xy) / max(uPointer.z, 1.0);
    float influence = pow(max(0.0, 1.0 - distanceToPointer), 2.6) * uPointer.w;
    color += vec3(0.38, 0.70, 1.0) * influence * intro;
  }

  float noise = hash(floor(gl_FragCoord.xy / max(uPixelRatio, 1.0))) - 0.5;
  color += noise * 0.0105 * intro;
  outColor = vec4(max(color, vec3(0.0)), 1.0);
}
`;

const particleVertexShader = `#version 300 es
precision highp float;

layout(location = 0) in vec4 aPositionSizeSpeed;
layout(location = 1) in vec4 aAppearance;

uniform float uPixelRatio;
uniform float uTime;
uniform float uIntro;

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
  vAlpha = aAppearance.x *
    (0.58 + 0.42 * ((sin(uTime * aAppearance.z + aAppearance.y) + 1.0) * 0.5)) *
    (1.0 - smoothstep(0.74, 1.0, position.y)) * 0.65 * clamp(uIntro, 0.0, 1.0);
}
`;

const particleFragmentShader = `#version 300 es
precision highp float;

in float vAlpha;
in float vBlur;
in float vCoreRadius;
out vec4 outColor;

void main() {
  vec2 point = gl_PointCoord * 2.0 - 1.0;
  float distanceFromCenter = length(point);
  if (distanceFromCenter > 1.0) discard;
  float core = 1.0 - smoothstep(max(0.05, vCoreRadius * 0.55), vCoreRadius,
    distanceFromCenter);
  float haloFalloff = mix(8.0, 3.25, clamp(vBlur / 1.8, 0.0, 1.0));
  float halo = exp(-distanceFromCenter * distanceFromCenter * haloFalloff) * (1.0 - core);
  float alpha = vAlpha * (core + halo * 0.42);
  vec3 color = mix(
    vec3(174.0, 207.0, 242.0) / 255.0,
    vec3(216.0, 236.0, 248.0) / 255.0,
    core
  );
  outColor = vec4(color * alpha, alpha);
}
`;

const surfaceVertexShader = `#version 300 es
precision highp float;

layout(location = 0) in vec2 aCorner;
layout(location = 1) in vec4 aRect;
layout(location = 2) in vec4 aAppearance;

uniform vec2 uResolution;

out vec4 vRect;
out float vRadius;
flat out float vKind;
out float vActivity;
out float vOpacity;

void main() {
  vec2 point = aRect.xy - 14.0 + aCorner * (aRect.zw + 28.0);
  vec2 clip = point / uResolution * 2.0 - 1.0;
  gl_Position = vec4(clip.x, -clip.y, 0.0, 1.0);
  vRect = aRect;
  vRadius = aAppearance.x;
  vKind = aAppearance.y;
  vActivity = aAppearance.z;
  vOpacity = aAppearance.w;
}
`;

const surfaceFragmentShader = `#version 300 es
precision highp float;

uniform sampler2D uContent;
uniform vec2 uResolution;
uniform float uPixelRatio;
uniform float uTime;
uniform float uHasContent;

in vec4 vRect;
in float vRadius;
flat in float vKind;
in float vActivity;
in float vOpacity;

out vec4 outColor;

float roundedBox(vec2 point, vec2 halfSize, float radius) {
  vec2 q = abs(point) - halfSize + radius;
  return length(max(q, 0.0)) + min(max(q.x, q.y), 0.0) - radius;
}

float angleDistance(float left, float right) {
  return abs(mod(left - right + 3.14159265, 6.2831853) - 3.14159265);
}

void main() {
  vec2 point = vec2(gl_FragCoord.x, uResolution.y * uPixelRatio - gl_FragCoord.y) /
    uPixelRatio;
  vec2 center = vRect.xy + vRect.zw * 0.5;
  vec2 local = point - center;
  vec2 halfSize = vRect.zw * 0.5;
  float distanceToEdge = roundedBox(local, halfSize, min(vRadius, min(halfSize.x, halfSize.y)));

  float face = 1.0 - smoothstep(-0.7, 0.7, distanceToEdge);
  float border = 1.0 - smoothstep(0.45, 1.45, abs(distanceToEdge));
  float outerGlow = exp(-max(distanceToEdge, 0.0) * 0.28) * step(0.0, distanceToEdge);
  vec2 normalized = local / max(halfSize, vec2(1.0));
  float perimeterAngle = atan(normalized.y, normalized.x);
  float travel = mod(uTime * (vKind < 2.0 ? 0.52 : 0.39), 6.2831853) - 3.14159265;
  float movingEdge = exp(-pow(angleDistance(perimeterAngle, travel) / 0.62, 2.0));
  float topReflection = pow(max(0.0, 1.0 - (normalized.y + 1.0) * 0.72), 3.0);

  vec3 fillColor;
  float fillAlpha;
  if (vKind < 1.5) {
    fillColor = mix(
      vec3(5.0, 6.0, 15.0) / 255.0,
      vec3(186.0, 207.0, 247.0) / 255.0,
      0.04
    );
    fillAlpha = 0.9984;
  } else if (vKind < 2.5) {
    fillColor = vec3(8.0, 10.0, 21.0) / 255.0;
    fillAlpha = 0.82;
  } else if (vKind < 3.5) {
    fillColor = vec3(186.0, 214.0, 247.0) / 255.0;
    fillAlpha = 0.06;
  } else {
    fillColor = vec3(16.0, 16.0, 23.0) / 255.0;
    fillAlpha = 0.82;
  }

  vec3 color = fillColor * fillAlpha * face;
  float alpha = fillAlpha * face;
  if (uHasContent > 0.5 && face > 0.0) {
    vec2 uv = point / uResolution;
    vec2 distortion = normalize(local + vec2(0.001)) *
      (1.0 - smoothstep(-18.0, -2.0, distanceToEdge)) * 0.0035;
    vec3 reflected = textureLod(uContent, uv + distortion, 1.5).rgb;
    color += reflected * face * 0.028;
  }

  vec3 ice = vec3(174.0, 207.0, 242.0) / 255.0;
  if (vKind < 1.5) color += ice * face * topReflection * 0.032;
  float baseEdge = vKind > 2.5 && vKind < 3.5 ? 0.12 : 0.02;
  float movingStrength = vKind < 1.5 ? 0.30 : 0.20;
  float edgeAlpha = border * (baseEdge + movingEdge *
    (movingStrength + vActivity * 0.22));
  edgeAlpha += border * topReflection * 0.08;
  edgeAlpha += outerGlow * movingEdge * (0.025 + vActivity * 0.055);
  color += ice * edgeAlpha;
  alpha = max(alpha, min(edgeAlpha, 0.92));
  outColor = vec4(color * vOpacity, alpha * vOpacity);
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

function compileShader(context: WebGL2RenderingContext, type: number, source: string) {
  const shader = context.createShader(type);
  if (!shader) throw new Error("Could not create canvas shader");
  context.shaderSource(shader, source);
  context.compileShader(shader);
  return shader;
}

async function createProgram(
  context: WebGL2RenderingContext,
  vertexSource: string,
  fragmentSource: string,
) {
  const vertexShader = compileShader(context, context.VERTEX_SHADER, vertexSource);
  const fragmentShader = compileShader(context, context.FRAGMENT_SHADER, fragmentSource);
  const program = context.createProgram();
  if (!program) throw new Error("Could not create canvas program");
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
      const message = context.getShaderInfoLog(shader) ?? "Unknown canvas shader error";
      context.deleteProgram(program);
      context.deleteShader(vertexShader);
      context.deleteShader(fragmentShader);
      throw new Error(message);
    }
  }
  if (!context.getProgramParameter(program, context.LINK_STATUS)) {
    const message = context.getProgramInfoLog(program) ?? "Unknown canvas program error";
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

function collectUniforms<Name extends string>(
  context: WebGL2RenderingContext,
  program: WebGLProgram,
  names: readonly Name[],
) {
  const result = {} as Record<Name, WebGLUniformLocation>;
  for (const name of names) {
    const location = context.getUniformLocation(program, name);
    if (!location) throw new Error(`Missing canvas uniform: ${name}`);
    result[name] = location;
  }
  return result;
}

function surfaceKind(element: HTMLElement) {
  switch (element.dataset.illuminationGlass) {
    case "panel":
      return 1;
    case "field":
      return 2;
    case "control":
      return 3;
    default:
      return 4;
  }
}

function effectiveOpacity(element: HTMLElement, boundary: HTMLElement) {
  let opacity = 1;
  let current: HTMLElement | null = element;
  while (current) {
    opacity *= Number.parseFloat(getComputedStyle(current).opacity) || 0;
    if (current === boundary) break;
    current = current.parentElement;
  }
  return opacity;
}

class WebGLCanvasRenderer implements CanvasRenderer {
  readonly mode: CanvasMode;
  readonly #content: HTMLElement;
  readonly #context: WebGL2RenderingContext;
  readonly #contentTexture: WebGLTexture;
  readonly #output: HTMLCanvasElement;
  readonly #particleBuffer: WebGLBuffer;
  readonly #particleVertexArray: WebGLVertexArrayObject;
  readonly #programs: readonly [WebGLProgram, WebGLProgram, WebGLProgram];
  readonly #reducedMotion: MediaQueryList;
  readonly #resizeObserver: ResizeObserver;
  readonly #mutationObserver: MutationObserver;
  readonly #scheduler: WakeScheduler;
  readonly #source: PaintableCanvas;
  readonly #sourceContext: ElementImageContext | null;
  readonly #surfaceCornerBuffer: WebGLBuffer;
  readonly #surfaceBuffer: WebGLBuffer;
  readonly #surfaceVertexArray: WebGLVertexArrayObject;
  readonly #uniforms: readonly [SceneUniforms, ParticleUniforms, SurfaceUniforms];
  #activeSurface: HTMLElement | null = null;
  #contentDirty = false;
  #frameCount = 0;
  #height = 1;
  #htmlInCanvas = false;
  #lastFrame = 0;
  #lastGeometryAnimationTime = -1;
  #layoutDirty = true;
  #particleCount = 0;
  #pixelRatio = 1;
  #pointer = { x: 0, y: 0, alpha: 0 };
  #pointerTarget: { x: number; y: number } | null = null;
  #startTime: number | null = null;
  #surfaces: Surface[] = [];
  #geometryAnimations: Animation[] = [];
  #width = 1;

  constructor(
    elements: CanvasElements,
    context: WebGL2RenderingContext,
    programs: readonly [WebGLProgram, WebGLProgram, WebGLProgram],
  ) {
    this.#content = elements.content;
    this.#context = context;
    this.#output = elements.output;
    this.#programs = programs;
    this.#source = elements.source as PaintableCanvas;
    this.#sourceContext = this.#source.getContext("2d") as ElementImageContext | null;
    this.#htmlInCanvas = canRenderHtmlInCanvas(this.#source, this.#sourceContext);
    this.mode = this.#htmlInCanvas ? "canvas-ui" : "webgl2";
    this.#uniforms = [
      collectUniforms(context, programs[0], [
        "uIntro",
        "uPixelRatio",
        "uPointer",
        "uResolution",
        "uTime",
      ]),
      collectUniforms(context, programs[1], ["uIntro", "uPixelRatio", "uTime"]),
      collectUniforms(context, programs[2], [
        "uContent",
        "uHasContent",
        "uPixelRatio",
        "uResolution",
        "uTime",
      ]),
    ];

    const particleBuffer = context.createBuffer();
    const particleVertexArray = context.createVertexArray();
    const surfaceCornerBuffer = context.createBuffer();
    const surfaceBuffer = context.createBuffer();
    const surfaceVertexArray = context.createVertexArray();
    const contentTexture = context.createTexture();
    if (
      !particleBuffer ||
      !particleVertexArray ||
      !surfaceCornerBuffer ||
      !surfaceBuffer ||
      !surfaceVertexArray ||
      !contentTexture
    ) {
      throw new Error("Could not allocate canvas GPU resources");
    }
    this.#particleBuffer = particleBuffer;
    this.#particleVertexArray = particleVertexArray;
    this.#surfaceCornerBuffer = surfaceCornerBuffer;
    this.#surfaceBuffer = surfaceBuffer;
    this.#surfaceVertexArray = surfaceVertexArray;
    this.#contentTexture = contentTexture;

    context.disable(context.DEPTH_TEST);
    context.disable(context.STENCIL_TEST);
    context.clearColor(0, 0, 0, 1);

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

    context.bindVertexArray(this.#surfaceVertexArray);
    context.bindBuffer(context.ARRAY_BUFFER, this.#surfaceCornerBuffer);
    context.bufferData(
      context.ARRAY_BUFFER,
      new Float32Array([0, 0, 1, 0, 0, 1, 0, 1, 1, 0, 1, 1]),
      context.STATIC_DRAW,
    );
    context.enableVertexAttribArray(0);
    context.vertexAttribPointer(0, 2, context.FLOAT, false, 2 * Float32Array.BYTES_PER_ELEMENT, 0);
    context.bindBuffer(context.ARRAY_BUFFER, this.#surfaceBuffer);
    context.enableVertexAttribArray(1);
    context.vertexAttribPointer(
      1,
      4,
      context.FLOAT,
      false,
      FLOATS_PER_SURFACE * Float32Array.BYTES_PER_ELEMENT,
      0,
    );
    context.vertexAttribDivisor(1, 1);
    context.enableVertexAttribArray(2);
    context.vertexAttribPointer(
      2,
      4,
      context.FLOAT,
      false,
      FLOATS_PER_SURFACE * Float32Array.BYTES_PER_ELEMENT,
      4 * Float32Array.BYTES_PER_ELEMENT,
    );
    context.vertexAttribDivisor(2, 1);
    context.bindVertexArray(null);

    context.bindTexture(context.TEXTURE_2D, this.#contentTexture);
    context.texParameteri(
      context.TEXTURE_2D,
      context.TEXTURE_MIN_FILTER,
      context.LINEAR_MIPMAP_LINEAR,
    );
    context.texParameteri(context.TEXTURE_2D, context.TEXTURE_MAG_FILTER, context.LINEAR);
    context.texParameteri(context.TEXTURE_2D, context.TEXTURE_WRAP_S, context.CLAMP_TO_EDGE);
    context.texParameteri(context.TEXTURE_2D, context.TEXTURE_WRAP_T, context.CLAMP_TO_EDGE);
    context.texImage2D(
      context.TEXTURE_2D,
      0,
      context.RGBA,
      1,
      1,
      0,
      context.RGBA,
      context.UNSIGNED_BYTE,
      new Uint8Array([0, 0, 0, 0]),
    );
    context.generateMipmap(context.TEXTURE_2D);

    this.#reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
    this.#scheduler = new WakeScheduler(
      () => this.#drawFrame(),
      (state) => {
        this.#output.dataset.state = state;
      },
    );
    this.#resizeObserver = new ResizeObserver(() => {
      this.#layoutDirty = true;
      this.#resize();
    });
    this.#resizeObserver.observe(this.#output);
    this.#resizeObserver.observe(this.#content);
    this.#mutationObserver = new MutationObserver(() => {
      this.#layoutDirty = true;
      if (this.#htmlInCanvas) this.#source.requestPaint?.();
      this.#scheduler.wake();
    });
    this.#mutationObserver.observe(this.#content, {
      attributeFilter: ["class", "data-illumination-active", "style"],
      attributes: true,
      childList: true,
      subtree: true,
    });

    this.#content.addEventListener("pointermove", this.#onPointerMove, { passive: true });
    this.#content.addEventListener("pointerleave", this.#onPointerLeave, { passive: true });
    // The content div is the page's only scroller and scrolling repaints
    // nothing the MutationObserver can see, so the composited frame would
    // freeze at the pre-scroll pose without an explicit wake.
    this.#content.addEventListener("scroll", this.#onScroll, { passive: true });
    this.#content.addEventListener("focusin", this.#onFocusChange);
    this.#content.addEventListener("focusout", this.#onFocusChange);
    window.addEventListener("visual-harness:seek", this.#onVisualSeek);
    document.addEventListener("visibilitychange", this.#onVisibilityChange);
    this.#reducedMotion.addEventListener("change", this.#onReducedMotionChange);

    if (this.#htmlInCanvas && this.#sourceContext) {
      this.#source.onpaint = () => {
        this.#captureContent();
        this.#scheduler.wake();
      };
    }

    this.#resize();
    this.#source.requestPaint?.();
  }

  readonly #onPointerMove = (event: PointerEvent) => {
    if (this.#reducedMotion.matches || document.visibilityState === "hidden") return;
    const rect = this.#output.getBoundingClientRect();
    this.#pointerTarget = { x: event.clientX - rect.left, y: event.clientY - rect.top };
    const target = event.target instanceof Element ? event.target : null;
    this.#activeSurface = target?.closest<HTMLElement>("[data-illumination-glass]") ?? null;
    this.#scheduler.wake();
  };

  readonly #onPointerLeave = () => {
    this.#pointerTarget = null;
    this.#activeSurface = null;
    this.#scheduler.wake();
  };

  readonly #onFocusChange = () => {
    this.#scheduler.wake();
  };

  readonly #onScroll = () => {
    this.#layoutDirty = true;
    if (this.#htmlInCanvas) this.#source.requestPaint?.();
    this.#scheduler.wake();
  };

  readonly #onVisualSeek = () => {
    this.#layoutDirty = true;
    this.#captureContent();
    this.#source.requestPaint?.();
    this.#drawFrame();
  };

  readonly #onReducedMotionChange = () => {
    this.#pointerTarget = null;
    this.#pointer.alpha = 0;
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
    this.#lastFrame = 0;
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

  #captureContent() {
    if (!this.#htmlInCanvas || !this.#sourceContext) return;
    try {
      this.#sourceContext.clearRect(0, 0, this.#source.width, this.#source.height);
      const transform = this.#sourceContext.drawElementImage?.(this.#content, 0, 0);
      const cssTransform = transform?.toString();
      if (cssTransform && this.#content.style.transform !== cssTransform) {
        this.#content.style.transform = cssTransform;
      }
      this.#contentDirty = true;
    } catch {
      this.#htmlInCanvas = false;
    }
  }

  #resize() {
    const { height, width } = this.#output.getBoundingClientRect();
    const pixelRatio = Math.min(window.devicePixelRatio || 1, MAX_PIXEL_RATIO);
    const backingWidth = Math.max(1, Math.round(width * pixelRatio));
    const backingHeight = Math.max(1, Math.round(height * pixelRatio));
    if (
      this.#output.width === backingWidth &&
      this.#output.height === backingHeight &&
      this.#width === width &&
      this.#height === height
    ) {
      return;
    }
    this.#height = height;
    this.#pixelRatio = pixelRatio;
    this.#width = width;
    this.#output.width = backingWidth;
    this.#output.height = backingHeight;
    this.#output.dataset.pixelRatio = pixelRatio.toFixed(2);
    this.#context.viewport(0, 0, backingWidth, backingHeight);
    this.#context.bindBuffer(this.#context.ARRAY_BUFFER, this.#particleBuffer);
    this.#context.bufferData(
      this.#context.ARRAY_BUFFER,
      this.#makeParticleData(width, height),
      this.#context.STATIC_DRAW,
    );
    if (this.#htmlInCanvas) {
      const sourcePixelRatio = Math.min(window.devicePixelRatio || 1, 2);
      const sourceWidth = Math.max(1, Math.round(this.#source.clientWidth * sourcePixelRatio));
      const sourceHeight = Math.max(1, Math.round(this.#source.clientHeight * sourcePixelRatio));
      if (this.#source.width !== sourceWidth) this.#source.width = sourceWidth;
      if (this.#source.height !== sourceHeight) this.#source.height = sourceHeight;
      this.#source.requestPaint?.();
    }
    this.#startTime = null;
    this.#lastFrame = 0;
    this.#layoutDirty = true;
    this.#drawFrame();
    if (!this.#reducedMotion.matches) this.#scheduler.wake();
  }

  #measureSurfaces() {
    this.#layoutDirty = false;
    const outputRect = this.#output.getBoundingClientRect();
    const previous = new Map(this.#surfaces.map((surface) => [surface.element, surface.activity]));
    this.#surfaces = [...this.#content.querySelectorAll<HTMLElement>("[data-illumination-glass]")]
      .map((element): Surface | null => {
        const rect = element.getBoundingClientRect();
        if (
          rect.width <= 0 ||
          rect.height <= 0 ||
          rect.bottom < outputRect.top ||
          rect.top > outputRect.bottom
        ) {
          return null;
        }
        const radius = Number.parseFloat(getComputedStyle(element).borderRadius) || 0;
        return {
          element,
          kind: surfaceKind(element),
          opacity: effectiveOpacity(element, this.#content),
          radius,
          rect: [rect.left - outputRect.left, rect.top - outputRect.top, rect.width, rect.height],
          activity: previous.get(element) ?? 0,
        };
      })
      .filter((surface): surface is Surface => surface !== null);
    this.#output.dataset.surfaceCount = String(this.#surfaces.length);
    this.#geometryAnimations = this.#content.getAnimations({ subtree: true });
  }

  #uploadContent() {
    if (!this.#htmlInCanvas || !this.#contentDirty) return;
    this.#contentDirty = false;
    const context = this.#context;
    context.bindTexture(context.TEXTURE_2D, this.#contentTexture);
    context.texImage2D(
      context.TEXTURE_2D,
      0,
      context.RGBA,
      context.RGBA,
      context.UNSIGNED_BYTE,
      this.#source,
    );
    context.generateMipmap(context.TEXTURE_2D);
  }

  #drawFrame() {
    const context = this.#context;
    if (context.isContextLost()) return false;
    if (this.#layoutDirty) this.#measureSurfaces();
    this.#uploadContent();

    const now = performance.now();
    this.#startTime ??= now;
    const visualSeekMs = Number.parseFloat(
      document.documentElement.dataset.visualHarnessSeekMs ?? "",
    );
    const elapsedSeconds = this.#reducedMotion.matches
      ? 0
      : Number.isFinite(visualSeekMs)
        ? visualSeekMs / 1000
        : (now - this.#startTime) / 1000;
    const animationTime = this.#geometryAnimations.reduce((total, animation) => {
      const current = animation.currentTime;
      return total + (typeof current === "number" ? current : 0);
    }, 0);
    if (animationTime !== this.#lastGeometryAnimationTime) {
      this.#lastGeometryAnimationTime = animationTime;
      this.#measureSurfaces();
    }
    const deltaSeconds =
      this.#lastFrame === 0 ? 1 / 60 : Math.min((now - this.#lastFrame) / 1000, 1 / 20);
    this.#lastFrame = now;
    const follow = this.#reducedMotion.matches ? 1 : 1 - Math.exp(-deltaSeconds * 24);
    const targetAlpha = this.#pointerTarget ? 1 : 0;
    if (this.#pointerTarget) {
      this.#pointer.x += (this.#pointerTarget.x - this.#pointer.x) * follow;
      this.#pointer.y += (this.#pointerTarget.y - this.#pointer.y) * follow;
    }
    this.#pointer.alpha += (targetAlpha - this.#pointer.alpha) * follow;
    const configuredIntro = Number.parseFloat(
      getComputedStyle(this.#content).getPropertyValue("--company-illumination"),
    );
    const introProgress = Number.isFinite(configuredIntro)
      ? Math.min(1, Math.max(0, configuredIntro))
      : 1;
    this.#output.dataset.introProgress = introProgress.toFixed(3);

    const [sceneProgram, particleProgram, surfaceProgram] = this.#programs;
    const [sceneUniforms, particleUniforms, surfaceUniforms] = this.#uniforms;
    context.disable(context.BLEND);
    context.useProgram(sceneProgram);
    context.uniform1f(sceneUniforms.uIntro, introProgress);
    context.uniform2f(sceneUniforms.uResolution, this.#width, this.#height);
    context.uniform1f(sceneUniforms.uPixelRatio, this.#pixelRatio);
    context.uniform1f(sceneUniforms.uTime, elapsedSeconds);
    context.uniform4f(
      sceneUniforms.uPointer,
      this.#pointer.x,
      this.#pointer.y,
      190,
      this.#pointer.alpha * 0.055,
    );
    context.drawArrays(context.TRIANGLES, 0, 3);

    context.enable(context.BLEND);
    context.blendEquation(context.FUNC_ADD);
    context.blendFunc(context.ONE, context.ONE);
    context.useProgram(particleProgram);
    context.uniform1f(particleUniforms.uIntro, introProgress);
    context.uniform1f(particleUniforms.uPixelRatio, this.#pixelRatio);
    context.uniform1f(particleUniforms.uTime, elapsedSeconds);
    context.bindVertexArray(this.#particleVertexArray);
    context.drawArrays(context.POINTS, 0, this.#particleCount);
    context.bindVertexArray(null);

    context.blendFunc(context.ONE, context.ONE_MINUS_SRC_ALPHA);
    context.useProgram(surfaceProgram);
    context.activeTexture(context.TEXTURE0);
    context.bindTexture(context.TEXTURE_2D, this.#contentTexture);
    context.uniform1i(surfaceUniforms.uContent, 0);
    context.uniform2f(surfaceUniforms.uResolution, this.#width, this.#height);
    context.uniform1f(surfaceUniforms.uPixelRatio, this.#pixelRatio);
    context.uniform1f(surfaceUniforms.uTime, elapsedSeconds);
    context.uniform1f(surfaceUniforms.uHasContent, this.#htmlInCanvas ? 1 : 0);
    const surfaceData = new Float32Array(this.#surfaces.length * FLOATS_PER_SURFACE);
    this.#surfaces.forEach((surface, index) => {
      const focused = surface.element.contains(document.activeElement);
      const explicit = surface.element.dataset.illuminationActive === "true";
      const target = surface.element === this.#activeSurface || focused || explicit ? 1 : 0;
      surface.activity += (target - surface.activity) * follow;
      surfaceData.set(
        [...surface.rect, surface.radius, surface.kind, surface.activity, surface.opacity],
        index * FLOATS_PER_SURFACE,
      );
    });
    context.bindBuffer(context.ARRAY_BUFFER, this.#surfaceBuffer);
    context.bufferData(context.ARRAY_BUFFER, surfaceData, context.DYNAMIC_DRAW);
    context.bindVertexArray(this.#surfaceVertexArray);
    context.drawArraysInstanced(context.TRIANGLES, 0, 6, this.#surfaces.length);
    context.bindVertexArray(null);

    this.#frameCount += 1;
    this.#output.dataset.frameCount = String(this.#frameCount);
    return !this.#reducedMotion.matches && document.visibilityState !== "hidden";
  }

  dispose() {
    this.#scheduler.dispose();
    this.#content.removeEventListener("pointermove", this.#onPointerMove);
    this.#content.removeEventListener("pointerleave", this.#onPointerLeave);
    this.#content.removeEventListener("scroll", this.#onScroll);
    this.#content.removeEventListener("focusin", this.#onFocusChange);
    this.#content.removeEventListener("focusout", this.#onFocusChange);
    window.removeEventListener("visual-harness:seek", this.#onVisualSeek);
    document.removeEventListener("visibilitychange", this.#onVisibilityChange);
    this.#reducedMotion.removeEventListener("change", this.#onReducedMotionChange);
    this.#resizeObserver.disconnect();
    this.#mutationObserver.disconnect();
    if (this.#htmlInCanvas) this.#source.onpaint = null;
    this.#context.deleteTexture(this.#contentTexture);
    this.#context.deleteBuffer(this.#particleBuffer);
    this.#context.deleteBuffer(this.#surfaceCornerBuffer);
    this.#context.deleteBuffer(this.#surfaceBuffer);
    this.#context.deleteVertexArray(this.#particleVertexArray);
    this.#context.deleteVertexArray(this.#surfaceVertexArray);
    for (const program of this.#programs) this.#context.deleteProgram(program);
  }
}

const cssRenderer: CanvasRenderer = {
  mode: "css",
  dispose() {},
};

export async function createCanvasRenderer(elements: CanvasElements): Promise<CanvasRenderer> {
  const context = elements.output.getContext("webgl2", contextOptions);
  if (!context) return cssRenderer;
  try {
    const programs = await Promise.all([
      createProgram(context, fullscreenVertexShader, sceneFragmentShader),
      createProgram(context, particleVertexShader, particleFragmentShader),
      createProgram(context, surfaceVertexShader, surfaceFragmentShader),
    ]);
    return new WebGLCanvasRenderer(
      elements,
      context,
      programs as [WebGLProgram, WebGLProgram, WebGLProgram],
    );
  } catch {
    return cssRenderer;
  }
}

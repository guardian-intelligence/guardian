import { type Rectangle, rectangleAround, toWebGLScissor, unionRectangles } from "./geometry";
import { WakeScheduler } from "./scheduler";

export type IlluminationMode = "css" | "webgl2";

export interface IlluminationRenderer {
  readonly mode: IlluminationMode;
  dispose(): void;
}

const contextOptions: WebGLContextAttributes = {
  alpha: true,
  antialias: false,
  depth: false,
  failIfMajorPerformanceCaveat: true,
  powerPreference: "high-performance",
  premultipliedAlpha: true,
  preserveDrawingBuffer: true,
  stencil: false,
};

const vertexShaderSource = `#version 300 es
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

const staticFragmentShaderSource = `#version 300 es
precision highp float;

uniform vec2 uCssResolution;

in vec2 vUv;
out vec4 outColor;

float hash21(vec2 point) {
  point = fract(point * vec2(123.34, 456.21));
  point += dot(point, point + 45.32);
  return fract(point.x * point.y);
}

float beam(vec2 point, vec2 origin, vec2 direction, float width, float reach) {
  vec2 offset = point - origin;
  float forward = dot(offset, direction);
  float lateral = abs(offset.x * direction.y - offset.y * direction.x);
  float cone = 1.0 - smoothstep(width * max(forward, 0.03), width * max(forward, 0.03) + 0.025, lateral);
  float head = smoothstep(0.0, 0.08, forward);
  float tail = 1.0 - smoothstep(reach * 0.72, reach, forward);
  return cone * head * tail;
}

float verticalLine(float x, float position, float opacity) {
  float distanceToLine = abs(x - position);
  return (1.0 - smoothstep(0.45, 1.35, distanceToLine)) * opacity;
}

void main() {
  vec2 uv = vec2(vUv.x, 1.0 - vUv.y);
  vec2 cssPoint = uv * uCssResolution;
  vec3 color = vec3(0.0196, 0.0235, 0.0588);

  vec2 stageOffset = (uv - vec2(0.5, 0.355)) / vec2(0.52, 0.67);
  float stageGlow = exp(-dot(stageOffset, stageOffset) * 2.5);
  color += vec3(0.018, 0.045, 0.16) * stageGlow;

  vec2 upperOffset = (uv - vec2(0.5, 0.31)) / vec2(0.5, 0.7);
  float upperGlow = exp(-dot(upperOffset, upperOffset) * 4.0);
  color += vec3(0.055, 0.074, 0.102) * upperGlow;

  vec2 origin = vec2(0.5, 0.09);
  float leftBeam = beam(uv, origin, normalize(vec2(-0.3, 1.0)), 0.19, 0.86);
  float centerBeam = beam(uv, origin, vec2(0.0, 1.0), 0.18, 0.9);
  float rightBeam = beam(uv, origin, normalize(vec2(0.3, 1.0)), 0.19, 0.86);
  color += vec3(0.078, 0.097, 0.135) * (leftBeam + rightBeam) * 0.22;
  color += vec3(0.09, 0.115, 0.16) * centerBeam * 0.2;

  float lineFade = (1.0 - smoothstep(0.66, 0.9, uv.y)) * smoothstep(0.0, 0.12, uv.y);
  float center = uCssResolution.x * 0.5;
  float lines =
    verticalLine(cssPoint.x, center - 520.0, 0.035) +
    verticalLine(cssPoint.x, center - 416.0, 0.06) +
    verticalLine(cssPoint.x, center, 0.028) +
    verticalLine(cssPoint.x, center + 416.0, 0.06) +
    verticalLine(cssPoint.x, center + 520.0, 0.035);
  float headerLine = (1.0 - smoothstep(0.45, 1.35, abs(cssPoint.y - 105.0))) * 0.055;
  headerLine *= smoothstep(0.0, 0.22, uv.x) * (1.0 - smoothstep(0.78, 1.0, uv.x));
  color += vec3(0.73, 0.84, 0.97) * (lines * lineFade + headerLine);

  vec2 particleCell = floor(cssPoint / 18.0);
  vec2 particleLocal = fract(cssPoint / 18.0);
  float particleSeed = hash21(particleCell);
  vec2 particlePosition = vec2(hash21(particleCell + 4.7), hash21(particleCell + 9.2));
  float particleDistance = length((particleLocal - particlePosition) * 18.0);
  float particle = (1.0 - smoothstep(0.3, 1.35, particleDistance));
  particle *= smoothstep(0.965, 0.998, particleSeed);
  particle *= 1.0 - smoothstep(0.68, 0.96, uv.y);
  color += vec3(0.68, 0.81, 0.95) * particle * (0.12 + particleSeed * 0.28);

  float grain = hash21(floor(gl_FragCoord.xy * 0.5));
  color += (grain - 0.5) * 0.006;

  vec2 vignetteUv = uv * (1.0 - uv.yx);
  float vignette = pow(clamp(vignetteUv.x * vignetteUv.y * 18.0, 0.0, 1.0), 0.18);
  color *= mix(0.58, 1.0, vignette);

  outColor = vec4(max(color, 0.0), 1.0);
}
`;

const dynamicFragmentShaderSource = `#version 300 es
precision highp float;

uniform int uLightCount;
uniform vec4 uLightColors[4];
uniform vec4 uLights[4];
uniform int uGlassCount;
uniform vec4 uGlassRects[6];
uniform vec4 uGlassParams[6];
uniform vec2 uCssResolution;
uniform sampler2D uStaticTexture;

in vec2 vUv;
out vec4 outColor;

float roundedRectangleDistance(
  vec2 point,
  vec2 center,
  vec2 halfSize,
  float radius
) {
  vec2 offset = abs(point - center) - halfSize + radius;
  return length(max(offset, 0.0)) + min(max(offset.x, offset.y), 0.0) - radius;
}

void main() {
  vec2 cssPoint = vec2(vUv.x, 1.0 - vUv.y) * uCssResolution;
  vec3 color = texture(uStaticTexture, vUv).rgb;

  for (int index = 0; index < 6; index++) {
    if (index >= uGlassCount) break;
    vec4 rectangle = uGlassRects[index];
    vec4 parameters = uGlassParams[index];
    vec2 center = rectangle.xy + rectangle.zw * 0.5;
    vec2 halfSize = rectangle.zw * 0.5;
    float distanceToSurface = roundedRectangleDistance(
      cssPoint,
      center,
      halfSize,
      min(parameters.x, min(halfSize.x, halfSize.y))
    );
    if (distanceToSurface <= 0.0) {
      vec2 normalized = (cssPoint - center) / max(halfSize, vec2(1.0));
      vec2 normal = normalize(normalized + vec2(0.0001));
      float rim = 1.0 - smoothstep(0.0, 16.0, -distanceToSurface);
      vec2 refraction = vec2(normal.x, -normal.y) * parameters.y / uCssResolution;
      vec3 refracted = vec3(
        texture(uStaticTexture, vUv + refraction * 1.12).r,
        texture(uStaticTexture, vUv + refraction).g,
        texture(uStaticTexture, vUv + refraction * 0.86).b
      );
      color = mix(color, refracted, parameters.z * (0.34 + rim * 0.3));
      color += vec3(0.36, 0.55, 0.82) * parameters.w * rim;
    }
  }

  for (int index = 0; index < 4; index++) {
    if (index >= uLightCount) break;
    vec4 light = uLights[index];
    vec3 lightColor = uLightColors[index].rgb;
    float distanceFromLight = distance(cssPoint, light.xy) / light.z;
    float influence = pow(max(0.0, 1.0 - distanceFromLight), 2.2) * light.w;
    color = 1.0 - (1.0 - color) * (1.0 - lightColor * influence);
  }

  outColor = vec4(color, 1.0);
}
`;

type Light = {
  readonly color: readonly [number, number, number];
  readonly intensity: number;
  readonly radius: number;
  readonly x: number;
  readonly y: number;
};

type GlassSurface = {
  readonly edge: number;
  readonly height: number;
  readonly radius: number;
  readonly refraction: number;
  readonly strength: number;
  readonly width: number;
  readonly x: number;
  readonly y: number;
};

const sourceLightStyles: Record<string, Pick<Light, "color" | "intensity" | "radius">> = {
  dropzone: { color: [0.36, 0.62, 1], intensity: 0.045, radius: 440 },
  logo: { color: [0.55, 0.72, 1], intensity: 0.17, radius: 170 },
  status: { color: [0.34, 0.9, 0.82], intensity: 0.075, radius: 120 },
};

const pointerLightStyle = {
  color: [0.38, 0.7, 1],
  intensity: 0.09,
  radius: 190,
} as const;

const glassSurfaceStyles: Record<
  string,
  Pick<GlassSurface, "edge" | "radius" | "refraction" | "strength">
> = {
  control: { edge: 0.055, radius: 999, refraction: 1.4, strength: 0.2 },
  field: { edge: 0.045, radius: 13, refraction: 1.8, strength: 0.24 },
  panel: { edge: 0.065, radius: 16, refraction: 3.2, strength: 0.32 },
};

function compileShader(context: WebGL2RenderingContext, type: number, source: string): WebGLShader {
  const shader = context.createShader(type);
  if (!shader) throw new Error("Could not create illumination shader");

  context.shaderSource(shader, source);
  context.compileShader(shader);
  return shader;
}

type ParallelShaderCompile = {
  readonly COMPLETION_STATUS_KHR: number;
};

function nextAnimationFrame() {
  return new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()));
}

async function createProgram(
  context: WebGL2RenderingContext,
  fragmentShaderSource: string,
): Promise<WebGLProgram> {
  const vertexShader = compileShader(context, context.VERTEX_SHADER, vertexShaderSource);
  const fragmentShader = compileShader(context, context.FRAGMENT_SHADER, fragmentShaderSource);
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
  readonly #dynamicProgram: WebGLProgram;
  readonly #staticProgram: WebGLProgram;
  readonly #framebuffer: WebGLFramebuffer;
  readonly #staticTexture: WebGLTexture;
  readonly #layoutResizeObserver: ResizeObserver;
  readonly #mutationObserver: MutationObserver;
  readonly #reducedMotion: MediaQueryList;
  readonly #resizeObserver: ResizeObserver;
  readonly #scheduler: WakeScheduler;
  #frameCount = 0;
  #glassElements: HTMLElement[] = [];
  #glassSurfaces: GlassSurface[] = [];
  #height = 1;
  #pendingFullDraw = false;
  #pendingRectangle: Rectangle | null = null;
  #pixelRatio = 1;
  #pointer: Light | null = null;
  #sourceElements: HTMLElement[] = [];
  #sources: Light[] = [];
  #width = 1;

  constructor(
    canvas: HTMLCanvasElement,
    context: WebGL2RenderingContext,
    dynamicProgram: WebGLProgram,
    staticProgram: WebGLProgram,
  ) {
    this.#canvas = canvas;
    this.#context = context;
    this.#dynamicProgram = dynamicProgram;
    this.#staticProgram = staticProgram;

    const framebuffer = context.createFramebuffer();
    const staticTexture = context.createTexture();
    if (!framebuffer || !staticTexture) throw new Error("Could not allocate illumination buffers");
    this.#framebuffer = framebuffer;
    this.#staticTexture = staticTexture;

    context.disable(context.BLEND);
    context.disable(context.DEPTH_TEST);
    context.disable(context.STENCIL_TEST);

    this.#reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
    this.#scheduler = new WakeScheduler(
      () => this.#drawPending(),
      (state) => {
        this.#canvas.dataset.state = state;
      },
    );
    this.#resizeObserver = new ResizeObserver(() => this.#resize());
    this.#layoutResizeObserver = new ResizeObserver(() => this.#queueLayoutSync());
    this.#mutationObserver = new MutationObserver(() => this.#refreshSceneElements());
    this.#resizeObserver.observe(canvas);
    this.#mutationObserver.observe(document.body, { childList: true, subtree: true });
    window.addEventListener("pointermove", this.#onPointerMove, { passive: true });
    window.addEventListener("scroll", this.#onLayoutChange, { passive: true });
    document.addEventListener("visibilitychange", this.#onVisibilityChange);
    document.documentElement.addEventListener("pointerleave", this.#onPointerLeave, {
      passive: true,
    });
    this.#reducedMotion.addEventListener("change", this.#onReducedMotionChange);
    this.#refreshSceneElements(false);
    this.#resize();
  }

  readonly #onPointerMove = (event: PointerEvent) => {
    if (this.#reducedMotion.matches || document.visibilityState === "hidden") return;
    const previous = this.#pointer;
    this.#pointer = {
      ...pointerLightStyle,
      x: event.clientX,
      y: event.clientY,
    };
    const previousRectangle = previous
      ? rectangleAround(previous.x, previous.y, previous.radius, this.#width, this.#height)
      : null;
    const nextRectangle = rectangleAround(
      this.#pointer.x,
      this.#pointer.y,
      this.#pointer.radius,
      this.#width,
      this.#height,
    );
    this.#queueRectangle(unionRectangles(previousRectangle, nextRectangle));
  };

  readonly #onLayoutChange = () => this.#queueLayoutSync();

  readonly #onReducedMotionChange = () => {
    const previous = this.#pointer;
    this.#pointer = null;
    if (previous) {
      this.#queueRectangle(
        rectangleAround(previous.x, previous.y, previous.radius, this.#width, this.#height),
      );
    }
  };

  readonly #onVisibilityChange = () => {
    if (document.visibilityState === "hidden") {
      this.#scheduler.suspend();
      return;
    }
    this.#scheduler.resume();
    this.#queueFullDraw();
  };

  readonly #onPointerLeave = () => {
    const previous = this.#pointer;
    this.#pointer = null;
    if (!previous) return;
    this.#queueRectangle(
      rectangleAround(previous.x, previous.y, previous.radius, this.#width, this.#height),
    );
  };

  #queueRectangle(rectangle: Rectangle | null) {
    if (!rectangle) return;
    this.#pendingRectangle = unionRectangles(this.#pendingRectangle, rectangle);
    this.#scheduler.wake();
  }

  #queueFullDraw() {
    this.#pendingFullDraw = true;
    this.#scheduler.wake();
  }

  #queueLayoutSync() {
    this.#syncSceneLayout();
    this.#queueFullDraw();
  }

  #refreshSceneElements(queueDraw = true) {
    this.#sourceElements = [
      ...document.querySelectorAll<HTMLElement>("[data-illumination-source]"),
    ];
    this.#glassElements = [...document.querySelectorAll<HTMLElement>("[data-illumination-glass]")];
    this.#layoutResizeObserver.disconnect();
    new Set([...this.#sourceElements, ...this.#glassElements]).forEach((element) => {
      this.#layoutResizeObserver.observe(element);
    });
    this.#syncSceneLayout();
    if (queueDraw) this.#queueFullDraw();
  }

  #drawPending() {
    if (this.#pendingFullDraw) {
      this.#pendingFullDraw = false;
      this.#pendingRectangle = null;
      this.#drawToScreen();
    } else {
      const pendingRectangle = this.#pendingRectangle;
      this.#pendingRectangle = null;
      if (pendingRectangle) this.#drawToScreen(pendingRectangle);
    }
    return this.#pendingFullDraw || this.#pendingRectangle !== null;
  }

  #resize() {
    const { height, width } = this.#canvas.getBoundingClientRect();
    const pixelRatio = Math.min(window.devicePixelRatio || 1, 1.25);
    const backingWidth = Math.max(1, Math.round(width * pixelRatio));
    const backingHeight = Math.max(1, Math.round(height * pixelRatio));

    if (this.#canvas.width === backingWidth && this.#canvas.height === backingHeight) return;
    this.#height = height;
    this.#pixelRatio = pixelRatio;
    this.#width = width;
    this.#canvas.width = backingWidth;
    this.#canvas.height = backingHeight;

    const context = this.#context;
    context.bindTexture(context.TEXTURE_2D, this.#staticTexture);
    context.texParameteri(context.TEXTURE_2D, context.TEXTURE_MIN_FILTER, context.LINEAR);
    context.texParameteri(context.TEXTURE_2D, context.TEXTURE_MAG_FILTER, context.LINEAR);
    context.texParameteri(context.TEXTURE_2D, context.TEXTURE_WRAP_S, context.CLAMP_TO_EDGE);
    context.texParameteri(context.TEXTURE_2D, context.TEXTURE_WRAP_T, context.CLAMP_TO_EDGE);
    context.texImage2D(
      context.TEXTURE_2D,
      0,
      context.RGBA8,
      backingWidth,
      backingHeight,
      0,
      context.RGBA,
      context.UNSIGNED_BYTE,
      null,
    );

    context.bindFramebuffer(context.FRAMEBUFFER, this.#framebuffer);
    context.framebufferTexture2D(
      context.FRAMEBUFFER,
      context.COLOR_ATTACHMENT0,
      context.TEXTURE_2D,
      this.#staticTexture,
      0,
    );
    if (context.checkFramebufferStatus(context.FRAMEBUFFER) !== context.FRAMEBUFFER_COMPLETE) {
      throw new Error("Illumination framebuffer is incomplete");
    }

    this.#drawStatic(width, height, backingWidth, backingHeight);
    this.#syncSceneLayout();
    this.#drawToScreen();
    this.#canvas.dataset.state = "idle";
  }

  #drawStatic(cssWidth: number, cssHeight: number, width: number, height: number) {
    const context = this.#context;
    context.bindFramebuffer(context.FRAMEBUFFER, this.#framebuffer);
    context.viewport(0, 0, width, height);
    context.useProgram(this.#staticProgram);
    context.uniform2f(
      requireUniform(context, this.#staticProgram, "uCssResolution"),
      cssWidth,
      cssHeight,
    );
    context.drawArrays(context.TRIANGLES, 0, 3);
  }

  #syncSceneLayout() {
    this.#sources = this.#sourceElements
      .map((element): Light | null => {
        const name = element.dataset.illuminationSource ?? "";
        const style = sourceLightStyles[name];
        if (!style) return null;
        const rectangle = element.getBoundingClientRect();
        return {
          ...style,
          x: rectangle.left + rectangle.width * 0.5,
          y: rectangle.top + rectangle.height * 0.5,
        };
      })
      .filter((light): light is Light => light !== null)
      .slice(0, 3);
    this.#glassSurfaces = this.#glassElements
      .map((element): GlassSurface | null => {
        const name = element.dataset.illuminationGlass ?? "";
        const style = glassSurfaceStyles[name];
        if (!style) return null;
        const rectangle = element.getBoundingClientRect();
        return {
          ...style,
          height: rectangle.height,
          width: rectangle.width,
          x: rectangle.left,
          y: rectangle.top,
        };
      })
      .filter((surface): surface is GlassSurface => surface !== null)
      .slice(0, 6);
  }

  #drawToScreen(dirtyRectangle?: Rectangle) {
    const context = this.#context;
    const lights = [...(this.#pointer ? [this.#pointer] : []), ...this.#sources].slice(0, 4);
    const lightValues = new Float32Array(16);
    const colorValues = new Float32Array(16);
    const glassRectangleValues = new Float32Array(24);
    const glassParameterValues = new Float32Array(24);
    lights.forEach((light, index) => {
      lightValues.set([light.x, light.y, light.radius, light.intensity], index * 4);
      colorValues.set([...light.color, 1], index * 4);
    });
    this.#glassSurfaces.forEach((surface, index) => {
      glassRectangleValues.set([surface.x, surface.y, surface.width, surface.height], index * 4);
      glassParameterValues.set(
        [surface.radius, surface.refraction, surface.strength, surface.edge],
        index * 4,
      );
    });

    context.bindFramebuffer(context.FRAMEBUFFER, null);
    context.viewport(0, 0, this.#canvas.width, this.#canvas.height);
    if (dirtyRectangle) {
      const scissor = toWebGLScissor(dirtyRectangle, this.#height, this.#pixelRatio);
      context.enable(context.SCISSOR_TEST);
      context.scissor(scissor.x, scissor.y, scissor.width, scissor.height);
    } else {
      context.disable(context.SCISSOR_TEST);
    }
    context.useProgram(this.#dynamicProgram);
    context.activeTexture(context.TEXTURE0);
    context.bindTexture(context.TEXTURE_2D, this.#staticTexture);
    context.uniform1i(requireUniform(context, this.#dynamicProgram, "uStaticTexture"), 0);
    context.uniform2f(
      requireUniform(context, this.#dynamicProgram, "uCssResolution"),
      this.#width,
      this.#height,
    );
    context.uniform1i(requireUniform(context, this.#dynamicProgram, "uLightCount"), lights.length);
    context.uniform1i(
      requireUniform(context, this.#dynamicProgram, "uGlassCount"),
      this.#glassSurfaces.length,
    );
    context.uniform4fv(requireUniform(context, this.#dynamicProgram, "uLights[0]"), lightValues);
    context.uniform4fv(
      requireUniform(context, this.#dynamicProgram, "uLightColors[0]"),
      colorValues,
    );
    context.uniform4fv(
      requireUniform(context, this.#dynamicProgram, "uGlassRects[0]"),
      glassRectangleValues,
    );
    context.uniform4fv(
      requireUniform(context, this.#dynamicProgram, "uGlassParams[0]"),
      glassParameterValues,
    );
    context.drawArrays(context.TRIANGLES, 0, 3);
    context.disable(context.SCISSOR_TEST);
    this.#frameCount += 1;
    this.#canvas.dataset.frameCount = String(this.#frameCount);
  }

  dispose() {
    this.#scheduler.dispose();
    window.removeEventListener("pointermove", this.#onPointerMove);
    window.removeEventListener("scroll", this.#onLayoutChange);
    document.removeEventListener("visibilitychange", this.#onVisibilityChange);
    document.documentElement.removeEventListener("pointerleave", this.#onPointerLeave);
    this.#reducedMotion.removeEventListener("change", this.#onReducedMotionChange);
    this.#mutationObserver.disconnect();
    this.#resizeObserver.disconnect();
    this.#layoutResizeObserver.disconnect();
    this.#context.deleteFramebuffer(this.#framebuffer);
    this.#context.deleteTexture(this.#staticTexture);
    this.#context.deleteProgram(this.#dynamicProgram);
    this.#context.deleteProgram(this.#staticProgram);
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
    const [dynamicProgram, staticProgram] = await Promise.all([
      createProgram(context, dynamicFragmentShaderSource),
      createProgram(context, staticFragmentShaderSource),
    ]);
    return new WebGLIlluminationRenderer(canvas, context, dynamicProgram, staticProgram);
  } catch {
    return cssRenderer;
  }
}

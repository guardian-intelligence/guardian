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

const copyFragmentShaderSource = `#version 300 es
precision highp float;

uniform sampler2D uStaticTexture;

in vec2 vUv;
out vec4 outColor;

void main() {
  outColor = texture(uStaticTexture, vUv);
}
`;

function compileShader(
  context: WebGL2RenderingContext,
  type: number,
  source: string,
): WebGLShader {
  const shader = context.createShader(type);
  if (!shader) throw new Error("Could not create illumination shader");

  context.shaderSource(shader, source);
  context.compileShader(shader);
  if (!context.getShaderParameter(shader, context.COMPILE_STATUS)) {
    const message = context.getShaderInfoLog(shader) ?? "Unknown shader compilation error";
    context.deleteShader(shader);
    throw new Error(message);
  }
  return shader;
}

function createProgram(
  context: WebGL2RenderingContext,
  fragmentShaderSource: string,
): WebGLProgram {
  const vertexShader = compileShader(context, context.VERTEX_SHADER, vertexShaderSource);
  const fragmentShader = compileShader(context, context.FRAGMENT_SHADER, fragmentShaderSource);
  const program = context.createProgram();
  if (!program) throw new Error("Could not create illumination program");

  context.attachShader(program, vertexShader);
  context.attachShader(program, fragmentShader);
  context.linkProgram(program);
  context.deleteShader(vertexShader);
  context.deleteShader(fragmentShader);

  if (!context.getProgramParameter(program, context.LINK_STATUS)) {
    const message = context.getProgramInfoLog(program) ?? "Unknown shader link error";
    context.deleteProgram(program);
    throw new Error(message);
  }
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
  readonly #copyProgram: WebGLProgram;
  readonly #staticProgram: WebGLProgram;
  readonly #framebuffer: WebGLFramebuffer;
  readonly #staticTexture: WebGLTexture;
  readonly #resizeObserver: ResizeObserver;
  #frameCount = 0;

  constructor(canvas: HTMLCanvasElement, context: WebGL2RenderingContext) {
    this.#canvas = canvas;
    this.#context = context;
    this.#copyProgram = createProgram(context, copyFragmentShaderSource);
    this.#staticProgram = createProgram(context, staticFragmentShaderSource);

    const framebuffer = context.createFramebuffer();
    const staticTexture = context.createTexture();
    if (!framebuffer || !staticTexture) throw new Error("Could not allocate illumination buffers");
    this.#framebuffer = framebuffer;
    this.#staticTexture = staticTexture;

    context.disable(context.BLEND);
    context.disable(context.DEPTH_TEST);
    context.disable(context.STENCIL_TEST);

    this.#resizeObserver = new ResizeObserver(() => this.#resize());
    this.#resizeObserver.observe(canvas);
    this.#resize();
  }

  #resize() {
    const { height, width } = this.#canvas.getBoundingClientRect();
    const pixelRatio = Math.min(window.devicePixelRatio || 1, 1.25);
    const backingWidth = Math.max(1, Math.round(width * pixelRatio));
    const backingHeight = Math.max(1, Math.round(height * pixelRatio));

    if (this.#canvas.width === backingWidth && this.#canvas.height === backingHeight) return;
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
    this.#drawToScreen(backingWidth, backingHeight);
    this.#frameCount += 1;
    this.#canvas.dataset.frameCount = String(this.#frameCount);
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

  #drawToScreen(width: number, height: number) {
    const context = this.#context;
    context.bindFramebuffer(context.FRAMEBUFFER, null);
    context.viewport(0, 0, width, height);
    context.useProgram(this.#copyProgram);
    context.activeTexture(context.TEXTURE0);
    context.bindTexture(context.TEXTURE_2D, this.#staticTexture);
    context.uniform1i(requireUniform(context, this.#copyProgram, "uStaticTexture"), 0);
    context.drawArrays(context.TRIANGLES, 0, 3);
  }

  dispose() {
    this.#resizeObserver.disconnect();
    this.#context.deleteFramebuffer(this.#framebuffer);
    this.#context.deleteTexture(this.#staticTexture);
    this.#context.deleteProgram(this.#copyProgram);
    this.#context.deleteProgram(this.#staticProgram);
  }
}

export function createIlluminationRenderer(canvas: HTMLCanvasElement): IlluminationRenderer {
  const context = canvas.getContext("webgl2", contextOptions);
  if (!context) {
    return {
      mode: "css",
      dispose() {},
    };
  }

  try {
    return new WebGLIlluminationRenderer(canvas, context);
  } catch {
    context.getExtension("WEBGL_lose_context")?.loseContext();
    return {
      mode: "css",
      dispose() {},
    };
  }
}

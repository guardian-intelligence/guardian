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

function resizeCanvas(canvas: HTMLCanvasElement, context: WebGL2RenderingContext) {
  const { height, width } = canvas.getBoundingClientRect();
  const pixelRatio = Math.min(window.devicePixelRatio || 1, 1.25);
  const backingWidth = Math.max(1, Math.round(width * pixelRatio));
  const backingHeight = Math.max(1, Math.round(height * pixelRatio));

  if (canvas.width !== backingWidth || canvas.height !== backingHeight) {
    canvas.width = backingWidth;
    canvas.height = backingHeight;
  }

  context.viewport(0, 0, backingWidth, backingHeight);
  context.clearColor(0, 0, 0, 0);
  context.clear(context.COLOR_BUFFER_BIT);
}

export function createIlluminationRenderer(canvas: HTMLCanvasElement): IlluminationRenderer {
  const context = canvas.getContext("webgl2", contextOptions);
  if (!context) {
    return {
      mode: "css",
      dispose() {},
    };
  }

  const resizeObserver = new ResizeObserver(() => resizeCanvas(canvas, context));
  resizeObserver.observe(canvas);
  resizeCanvas(canvas, context);

  return {
    mode: "webgl2",
    dispose() {
      resizeObserver.disconnect();
      context.getExtension("WEBGL_lose_context")?.loseContext();
    },
  };
}

export type PaintableCanvas = HTMLCanvasElement & {
  onpaint?: (() => void) | null;
  requestPaint?: () => void;
};

export type ElementImageContext = CanvasRenderingContext2D & {
  drawElementImage?: (element: Element, x: number, y: number) => DOMMatrix;
};

export function canRenderHtmlInCanvas(
  canvas: Pick<PaintableCanvas, "requestPaint">,
  context: Pick<ElementImageContext, "drawElementImage"> | null,
) {
  return Boolean(
    context &&
    typeof context.drawElementImage === "function" &&
    typeof canvas.requestPaint === "function",
  );
}

export function supportsHtmlInCanvas() {
  if (typeof document === "undefined") return false;
  const canvas = document.createElement("canvas") as PaintableCanvas;
  const context = canvas.getContext("2d") as ElementImageContext | null;
  return canRenderHtmlInCanvas(canvas, context);
}

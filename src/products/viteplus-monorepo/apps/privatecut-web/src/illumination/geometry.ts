export interface Rectangle {
  readonly height: number;
  readonly width: number;
  readonly x: number;
  readonly y: number;
}

export function rectangleAround(
  x: number,
  y: number,
  radius: number,
  width: number,
  height: number,
): Rectangle {
  const left = Math.max(0, x - radius);
  const top = Math.max(0, y - radius);
  const right = Math.min(width, x + radius);
  const bottom = Math.min(height, y + radius);
  return {
    height: Math.max(0, bottom - top),
    width: Math.max(0, right - left),
    x: left,
    y: top,
  };
}

export function unionRectangles(
  left: Rectangle | null,
  right: Rectangle | null,
): Rectangle | null {
  if (!left) return right;
  if (!right) return left;
  const x = Math.min(left.x, right.x);
  const y = Math.min(left.y, right.y);
  const farX = Math.max(left.x + left.width, right.x + right.width);
  const farY = Math.max(left.y + left.height, right.y + right.height);
  return { height: farY - y, width: farX - x, x, y };
}

export function toWebGLScissor(
  rectangle: Rectangle,
  cssHeight: number,
  pixelRatio: number,
): Rectangle {
  const x = Math.floor(rectangle.x * pixelRatio);
  const y = Math.floor((cssHeight - rectangle.y - rectangle.height) * pixelRatio);
  const farX = Math.ceil((rectangle.x + rectangle.width) * pixelRatio);
  const farY = Math.ceil((cssHeight - rectangle.y) * pixelRatio);
  return {
    height: Math.max(0, farY - y),
    width: Math.max(0, farX - x),
    x,
    y,
  };
}

export type MaterializationPixel = {
  readonly activation: number;
  readonly column: number;
  readonly normalizedX: number;
  readonly normalizedY: number;
  readonly row: number;
  readonly x: number;
  readonly y: number;
};

export type PixelLightState = "off" | "on" | "spotlight";

const UINT32_MAX = 0xffff_ffff;

export function clamp01(value: number) {
  return Math.min(1, Math.max(0, value));
}

export function smoothstep(start: number, end: number, value: number) {
  const progress = clamp01((value - start) / (end - start));
  return progress * progress * (3 - 2 * progress);
}

export function deterministicNoise(x: number, y: number, seed = 0x4755_4152) {
  let value = Math.imul(x ^ seed, 0x1f12_3bb5) ^ Math.imul(y + seed, 0x5f35_6495);
  value = Math.imul(value ^ (value >>> 15), 0x2c1b_3c6d);
  value = Math.imul(value ^ (value >>> 12), 0x297a_2d39);
  return ((value ^ (value >>> 15)) >>> 0) / UINT32_MAX;
}

export function activationThreshold(
  column: number,
  row: number,
  normalizedX: number,
  normalizedY: number,
) {
  const distance = clamp01(Math.hypot(normalizedX * 0.9, normalizedY * 0.52));
  const clusterNoise = deterministicNoise(Math.floor(column / 3), Math.floor(row / 3));
  const cellNoise = deterministicNoise(column, row, 0x444f_5453);
  return clamp01(distance * 0.65 + clusterNoise * 0.25 + cellNoise * 0.1);
}

export function spotlightEdge(normalizedY: number, progress: number, side: -1 | 1) {
  const wave =
    Math.sin(normalizedY * 7.4 + side * 0.8) * 0.62 +
    Math.sin(normalizedY * 17.2 - side * 1.3) * 0.25 +
    Math.sin(normalizedY * 31.6 + side * 2.1) * 0.13;
  return clamp01(progress * (1 + wave * 0.065));
}

export function pixelLightState(pixel: MaterializationPixel, progress: number): PixelLightState {
  const fill = clamp01(progress);
  if (pixel.activation > fill) return "off";
  const side = pixel.normalizedX < 0 ? -1 : 1;
  return Math.abs(pixel.normalizedX) <= spotlightEdge(pixel.normalizedY, fill, side)
    ? "spotlight"
    : "on";
}

export function materializationPixelOpacity(progress: number) {
  return 1 - smoothstep(0.82, 1, progress);
}

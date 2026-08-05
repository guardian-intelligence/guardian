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

export type SpotlightMask = {
  readonly left: number;
  readonly leftTrail: number;
  readonly opacity: number;
  readonly right: number;
  readonly rightTrail: number;
  readonly trailWidth: number;
  readonly width: number;
};

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

export function spotlightMask(progress: number): SpotlightMask {
  const fill = clamp01(progress);
  const travel = Math.pow(fill, 1.16);
  const width = 0.25 + smoothstep(0.02, 0.34, fill) * 10.25;
  const left = 50 - travel * 70;
  const right = 50 + travel * 70;
  return {
    left,
    leftTrail: left + 12,
    opacity: smoothstep(0.02, 0.12, fill),
    right,
    rightTrail: right - 12,
    trailWidth: width * 0.68,
    width,
  };
}

function insideEllipse(
  x: number,
  y: number,
  centerX: number,
  centerY: number,
  radiusX: number,
  radiusY: number,
) {
  return Math.hypot((x - centerX) / radiusX, (y - centerY) / radiusY) <= 1;
}

export function pixelInSpotlightMask(pixel: MaterializationPixel, mask: SpotlightMask) {
  const x = (pixel.normalizedX + 1) * 50;
  const y = (pixel.normalizedY + 1) * 50;
  return (
    insideEllipse(x, y, mask.left, 38, mask.width, 82) ||
    insideEllipse(x, y, mask.right, 42, mask.width, 82) ||
    insideEllipse(x, y, mask.leftTrail, 68, mask.trailWidth, 58) ||
    insideEllipse(x, y, mask.rightTrail, 72, mask.trailWidth, 58)
  );
}

export function pixelLightState(pixel: MaterializationPixel, progress: number): PixelLightState {
  const fill = clamp01(progress);
  if (pixel.activation > fill) return "off";
  return pixelInSpotlightMask(pixel, spotlightMask(fill)) ? "spotlight" : "on";
}

export function materializationPixelOpacity(progress: number) {
  return 1 - smoothstep(0.48, 0.98, progress);
}

export function shimmerPixelIntensity(pixel: MaterializationPixel, progress: number) {
  const shimmer = clamp01(progress);
  const envelope = smoothstep(0, 0.045, shimmer) * (1 - smoothstep(0.94, 1, shimmer));
  if (envelope <= 0) return 0;
  const noise = deterministicNoise(pixel.column, pixel.row, 0x5348_494d);
  const distanceFromCenter = Math.abs(pixel.normalizedX);
  const strand = Math.sin(
    pixel.normalizedY * Math.PI * 5.5 + distanceFromCenter * Math.PI * 2.25 + noise * 1.4,
  );
  const wovenDistance = Math.max(0, distanceFromCenter + strand * 0.055 + (noise - 0.5) * 0.035);
  const front = smoothstep(0.015, 0.92, shimmer) * 1.45;
  const distanceFromFront = wovenDistance - front;
  const wavefront = Math.exp(-0.5 * Math.pow(distanceFromFront / 0.1, 2));
  const arrival = smoothstep(wovenDistance - 0.09, wovenDistance + 0.035, front);
  const recession = 1 - smoothstep(0.1, 0.24, front - wovenDistance);
  const trailingThread = arrival * recession * (0.1 + noise * 0.16);
  const weaveGrain = 0.72 + noise * 0.28;

  return clamp01(Math.max(wavefront * weaveGrain, trailingThread) * envelope);
}

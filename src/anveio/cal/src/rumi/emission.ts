import { BUFFER, MAP_MODE_READ, TEXTURE } from "../gpu/gpu.ts";

// Rumi's light, as she emits it toward the far wall behind her: one texel per
// direction around her (texel i is screen angle 2*pi*i/TEXELS, y up, 0 = right),
// sRGB-encoded. orb.wgsl's fs_emission raymarches it each frame; ink.wgsl
// samples it for the wall, and a small CPU copy lights the page's glass.

export const TEXELS = 64; // 64 rgba8 texels = 256 bytes, the row alignment a copy needs

type Shared = { device: GPUDevice; texture: GPUTexture; read: GPUBuffer };
let shared: Shared | null = null;
let colours: Uint8Array | null = null;
let reading = false;

function ensure(device: GPUDevice): Shared {
  if (shared?.device === device) return shared;
  shared = {
    device,
    texture: device.createTexture({
      label: "rumi emission",
      size: [TEXELS, 1],
      format: "rgba8unorm",
      usage: TEXTURE.RENDER_ATTACHMENT | TEXTURE.TEXTURE_BINDING | TEXTURE.COPY_SRC,
    }),
    read: device.createBuffer({
      label: "rumi emission readback",
      size: TEXELS * 4,
      usage: BUFFER.MAP_READ | BUFFER.COPY_DST,
    }),
  };
  return shared;
}

export const emissionTexture = (device: GPUDevice): GPUTexture => ensure(device).texture;

/**
 * Copies this frame's emission for the CPU, unless a copy is still on its way
 * back; call `collect` once the encoder is submitted.
 */
export function readEmission(device: GPUDevice, enc: GPUCommandEncoder): (() => void) | null {
  if (reading) return null;
  const s = ensure(device);
  reading = true;
  enc.copyTextureToBuffer({ texture: s.texture }, { buffer: s.read, bytesPerRow: TEXELS * 4 }, [
    TEXELS,
    1,
  ]);
  return () => {
    s.read.mapAsync(MAP_MODE_READ).then(
      () => {
        colours = new Uint8Array(s.read.getMappedRange().slice(0));
        s.read.unmap();
        reading = false;
      },
      () => {
        reading = false;
      },
    );
  };
}

// how much her light off the far wall leans toward the face of her turned a
// given way, rather than her average (ink.wgsl's FACE_TINT)
const FACE_TINT = 0.3;

/**
 * Her light off the far wall toward screen angle `angle` (rad, y up) as
 * "r g b" 0-255: her average, tinted toward that face. Null until the first
 * frame has come back.
 */
export function emissionAt(angle: number): string | null {
  if (!colours) return null;
  const c = colours;
  const turn = (((angle / (Math.PI * 2)) % 1) + 1) % 1;
  const i = Math.round(turn * TEXELS) % TEXELS;
  // average in linear light, as the wall does
  const lin = (v: number) => (v / 255) ** 2.2;
  return [0, 1, 2]
    .map((ch) => {
      let mean = 0;
      for (let j = 0; j < TEXELS; j++) mean += lin(c[j * 4 + ch] ?? 0);
      const v = mean / TEXELS + (lin(c[i * 4 + ch] ?? 0) - mean / TEXELS) * FACE_TINT;
      return Math.round(v ** (1 / 2.2) * 255);
    })
    .join(" ");
}

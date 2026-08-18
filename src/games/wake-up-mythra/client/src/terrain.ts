// A terrain artifact is a header followed by per-cell planes, all
// contiguous. The sim parses it itself (sim_set_terrain); this decode
// exists only so a renderer can read the same bytes without a second
// fetch or a wasm round trip. The planes are views into the blob, not
// copies — the blob outlives them, and it is reloaded verbatim into every
// fresh park instance a module swap creates.

/** Little-endian "MYT1", the artifact magic the Rust and Go readers also demand. */
const MAGIC = 0x3154594d;
const SCHEMA = 1;

/** Offset of the first plane. Everything before it is the header. */
const PLANES_AT = 16;

export type TerrainPlanes = {
  readonly w: number;
  readonly h: number;
  /** Cell class: 0 block, 1 walkable, 2 water, 3 stairs. One byte per cell. */
  readonly ground: Uint8Array;
  /** Elevation level. One byte per cell. */
  readonly elev: Uint8Array;
  /** Deck level, 0 for none. One byte per cell. */
  readonly deck: Uint8Array;
  /** Sub-cell occupancy, a 16-bit mask per cell in a 4x4 grid: two bytes per cell. */
  readonly obstacle: Uint8Array;
  /** Cosmetic variant selector. One byte per cell. */
  readonly variant: Uint8Array;
  /** The artifact as fetched, for reloading into a swapped-in module instance. */
  readonly blob: Uint8Array;
};

/** Bytes a well-formed artifact of `w`x`h` occupies: header plus six cell-sized planes. */
export function terrainBytes(w: number, h: number): number {
  return PLANES_AT + 6 * w * h;
}

export class TerrainError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "TerrainError";
  }
}

/**
 * Slices a fetched artifact into renderer planes. Throws on a blob that
 * is not a schema-1 MYT1 artifact or is shorter than its own dimensions
 * demand — a truncated or mis-served fetch would otherwise surface as a
 * silently blank world.
 */
export function decodeTerrain(blob: Uint8Array): TerrainPlanes {
  if (blob.length < PLANES_AT) {
    throw new TerrainError(`terrain blob is ${blob.length}B, shorter than its header`);
  }
  const dv = new DataView(blob.buffer, blob.byteOffset, blob.byteLength);
  if (dv.getUint32(0, true) !== MAGIC) {
    throw new TerrainError("terrain blob does not carry the MYT1 magic");
  }
  const schema = dv.getUint32(4, true);
  if (schema !== SCHEMA) {
    throw new TerrainError(`terrain blob is schema ${schema}; this reader knows ${SCHEMA}`);
  }
  const w = dv.getUint16(8, true);
  const h = dv.getUint16(10, true);
  const cells = w * h;
  const want = terrainBytes(w, h);
  if (blob.length < want) {
    throw new TerrainError(`terrain blob is ${blob.length}B, ${w}x${h} needs ${want}B`);
  }
  return {
    w,
    h,
    ground: blob.subarray(PLANES_AT, PLANES_AT + cells),
    elev: blob.subarray(PLANES_AT + cells, PLANES_AT + 2 * cells),
    deck: blob.subarray(PLANES_AT + 2 * cells, PLANES_AT + 3 * cells),
    // The obstacle plane is two bytes per cell, so it spans three
    // cell-widths and the variant plane starts five in.
    obstacle: blob.subarray(PLANES_AT + 3 * cells, PLANES_AT + 5 * cells),
    variant: blob.subarray(PLANES_AT + 5 * cells, PLANES_AT + 6 * cells),
    blob,
  };
}

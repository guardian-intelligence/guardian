import { describe, expect, it } from "vitest";
import { TerrainError, decodeTerrain, terrainBytes } from "../src/terrain.ts";

/** Builds an artifact whose every plane byte names the plane it belongs to. */
function artifact(w: number, h: number): Uint8Array {
  const blob = new Uint8Array(terrainBytes(w, h));
  const dv = new DataView(blob.buffer);
  blob.set(new TextEncoder().encode("MYT1"), 0);
  dv.setUint32(4, 1, true);
  dv.setUint16(8, w, true);
  dv.setUint16(10, h, true);
  const cells = w * h;
  const marks = [
    [16, cells, 1], // ground
    [16 + cells, cells, 2], // elev
    [16 + 2 * cells, cells, 3], // deck
    [16 + 3 * cells, 2 * cells, 4], // obstacle, two bytes per cell
    [16 + 5 * cells, cells, 5], // variant
  ] as const;
  for (const [at, len, mark] of marks) blob.fill(mark, at, at + len);
  return blob;
}

describe("decodeTerrain", () => {
  it("reads the dimensions out of the header", () => {
    const t = decodeTerrain(artifact(40, 24));
    expect([t.w, t.h]).toEqual([40, 24]);
  });

  it("slices each plane at its own offset", () => {
    const w = 7;
    const h = 5;
    const cells = w * h;
    const t = decodeTerrain(artifact(w, h));
    expect(t.ground).toHaveLength(cells);
    expect(t.elev).toHaveLength(cells);
    expect(t.deck).toHaveLength(cells);
    // The obstacle plane is a 16-bit sub-cell mask, so it is twice as wide
    // as the rest and the variant plane starts five cell-widths in.
    expect(t.obstacle).toHaveLength(2 * cells);
    expect(t.variant).toHaveLength(cells);
    expect([...new Set(t.ground)]).toEqual([1]);
    expect([...new Set(t.elev)]).toEqual([2]);
    expect([...new Set(t.deck)]).toEqual([3]);
    expect([...new Set(t.obstacle)]).toEqual([4]);
    expect([...new Set(t.variant)]).toEqual([5]);
  });

  it("views the blob rather than copying it", () => {
    const blob = artifact(4, 4);
    const t = decodeTerrain(blob);
    expect(t.blob).toBe(blob);
    blob[16] = 99;
    expect(t.ground[0]).toBe(99);
  });

  it("decodes a blob that sits at a non-zero offset in its buffer", () => {
    // A fetch response sliced out of a larger buffer must still read its
    // own header, not the buffer's first bytes.
    const outer = new Uint8Array(64 + terrainBytes(3, 3));
    const inner = outer.subarray(64);
    inner.set(artifact(3, 3));
    const t = decodeTerrain(inner);
    expect([t.w, t.h]).toEqual([3, 3]);
    expect([...new Set(t.variant)]).toEqual([5]);
  });

  it("refuses a blob without the MYT1 magic", () => {
    // The same refusal the Rust reader and the Go server make: bytes that
    // are not a terrain artifact must not be sliced into planes as if
    // they were one — an error page fetched as terrain would otherwise
    // render as geography.
    const blob = artifact(4, 4);
    blob[0] = 0x58;
    expect(() => decodeTerrain(blob)).toThrow(/MYT1/);
  });

  it("refuses a schema this reader does not know", () => {
    const blob = artifact(4, 4);
    new DataView(blob.buffer).setUint32(4, 2, true);
    expect(() => decodeTerrain(blob)).toThrow(/schema 2/);
  });

  it("refuses a blob shorter than its own dimensions demand", () => {
    const blob = artifact(8, 8);
    expect(() => decodeTerrain(blob.subarray(0, blob.length - 1))).toThrow(TerrainError);
    expect(() => decodeTerrain(new Uint8Array(8))).toThrow(/shorter than its header/);
  });

  it("tolerates trailing bytes the schema may add later", () => {
    const blob = artifact(4, 4);
    const padded = new Uint8Array(blob.length + 32);
    padded.set(blob);
    expect(decodeTerrain(padded).w).toBe(4);
  });
});

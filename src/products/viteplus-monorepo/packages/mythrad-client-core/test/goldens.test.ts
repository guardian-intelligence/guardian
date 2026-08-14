// The record layouts cross the wasm boundary as raw little-endian bytes:
// the Rust writers place fields, these decoders find them. The goldens
// are bytes those writers actually produced, stamped with their decoded
// fields on the Rust side — so every offset and width here is tested
// against the writer, not trusted to match its doc comment.

import { describe, expect, it } from "vitest";
import goldens from "@guardian/chunkies-testkit/goldens/records.json";
import { decodeDiag, decodeHud, decodeViewDog, DIAG_BYTES, HUD_BYTES } from "../src/abi.ts";
import { decodeTerrain, terrainBytes } from "../src/terrain.ts";

function bytesOf(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  return out;
}

describe("record-layout goldens", () => {
  it("decodes the sim_hud record the park wrote", () => {
    const g = goldens.hud;
    const bytes = bytesOf(g.hex);
    expect(bytes.length).toBe(HUD_BYTES);
    expect(decodeHud(bytes)).toEqual({
      present: g.present,
      checkedInToday: g.checkedInToday,
      day: g.day,
      dogCount: g.dogCount,
      parkEnergy: BigInt(g.parkEnergy),
      selfEnergy: g.selfEnergy,
      boosting: g.boosting,
    });
  });

  it("decodes the session_diag record the session wrote", () => {
    const g = goldens.diag;
    const bytes = bytesOf(g.hex);
    expect(bytes.length).toBe(DIAG_BYTES);
    expect(decodeDiag(bytes)).toEqual({
      clockState: g.clockState,
      rttMs: g.rttMs,
      trailTicks: Number(BigInt(g.trailQ16)) / 65536,
      errorTicks: Number(BigInt(g.errorQ16)) / 65536,
      tick: BigInt(g.tick),
      seq: BigInt(g.seq),
      trailTargetTicks: g.trailTargetTicks,
      cushionTicks: g.cushionTicks,
      events: g.events,
      rollbacks: g.rollbacks,
      resyncs: g.resyncs,
      checks: g.checks,
      mismatches: g.mismatches,
      rejects: g.rejects,
    });
  });

  it("decodes the sim_view dog record the park wrote", () => {
    const g = goldens.view;
    const bytes = bytesOf(g.hex);
    const dv = new DataView(bytes.buffer);
    expect(dv.getUint32(0, true)).toBe(g.count);
    expect(decodeViewDog(bytes, 0)).toEqual({
      id: BigInt(g.dog.id),
      x: g.dog.x,
      y: g.dog.y,
      flags: g.dog.flags,
      facing: g.dog.facing,
      anim: g.dog.anim,
    });
  });

  it("decodes the terrain artifact the builder wrote", () => {
    const g = goldens.terrain;
    const blob = bytesOf(g.hex);
    expect(blob.length).toBe(terrainBytes(g.w, g.h));
    const t = decodeTerrain(blob);
    expect({ w: t.w, h: t.h }).toEqual({ w: g.w, h: g.h });
    const idx = (c: { x: number; y: number }): number => c.y * g.w + c.x;
    expect(t.ground[idx(g.swimCell)]).toBe(2);
    const at = idx(g.obstacle) * 2;
    expect(t.obstacle[at]! | (t.obstacle[at + 1]! << 8)).toBe(g.obstacle.mask);
    expect(t.variant[idx(g.variant)]).toBe(g.variant.value);
  });
});

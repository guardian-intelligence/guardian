// The diagnostics record crosses the wasm boundary as raw little-endian
// bytes: the Rust writer places fields, this decoder finds them. The
// golden is bytes that writer actually produced, stamped with its decoded
// fields on the Rust side — so every offset and width here is tested
// against the writer, not trusted to match its doc comment. The game
// records (hud, view, terrain) are pinned the same way in wum-client.

import { describe, expect, it } from "vitest";
import goldens from "@guardian/chunkies-testkit/goldens/records.json";
import { decodeDiag, DIAG_BYTES } from "../src/diag.ts";
import { clockStateName } from "../src/status.ts";

function bytesOf(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  return out;
}

describe("record-layout goldens", () => {
  it("decodes the session_diag record the session wrote", () => {
    const g = goldens.diag;
    const bytes = bytesOf(g.hex);
    expect(bytes.length).toBe(DIAG_BYTES);
    expect(decodeDiag(bytes)).toEqual({
      clockState: clockStateName(g.clockState),
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
});

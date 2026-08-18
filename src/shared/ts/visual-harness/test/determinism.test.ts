import { runInNewContext } from "node:vm";
import { describe, expect, it } from "vitest";
import { seededRandomInitScript } from "../src/determinism.ts";

function sequenceFor(seed: number, count: number): number[] {
  const sandbox = { Math: Object.create(Math) as Math };
  runInNewContext(seededRandomInitScript(seed), sandbox);
  return Array.from({ length: count }, () => sandbox.Math.random());
}

describe("seededRandomInitScript", () => {
  it("produces an identical sequence for the same seed", () => {
    expect(sequenceFor(1, 32)).toEqual(sequenceFor(1, 32));
  });

  it("produces distinct sequences for distinct seeds", () => {
    expect(sequenceFor(1, 8)).not.toEqual(sequenceFor(2, 8));
  });

  it("stays within [0, 1)", () => {
    for (const value of sequenceFor(7, 256)) {
      expect(value).toBeGreaterThanOrEqual(0);
      expect(value).toBeLessThan(1);
    }
  });
});

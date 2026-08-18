import { describe, expect, it } from "vitest";
import { BoundedGate } from "./bounded-gate";

describe("BoundedGate", () => {
  it("admits only the configured number of concurrent operations", () => {
    const gate = new BoundedGate(2);
    const releaseFirst = gate.tryEnter();
    const releaseSecond = gate.tryEnter();

    expect(releaseFirst).not.toBeNull();
    expect(releaseSecond).not.toBeNull();
    expect(gate.tryEnter()).toBeNull();

    releaseFirst?.();
    const releaseReplacement = gate.tryEnter();
    expect(releaseReplacement).not.toBeNull();

    releaseFirst?.();
    expect(gate.tryEnter()).toBeNull();

    releaseSecond?.();
    releaseReplacement?.();
    expect(gate.tryEnter()).not.toBeNull();
  });

  it("rejects an invalid limit", () => {
    expect(() => new BoundedGate(0)).toThrow();
    expect(() => new BoundedGate(1.5)).toThrow();
  });
});

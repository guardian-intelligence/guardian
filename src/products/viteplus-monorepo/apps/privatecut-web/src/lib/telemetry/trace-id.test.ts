import { describe, expect, it } from "vitest";
import { traceIdToBase64 } from "./trace-id";

describe("traceIdToBase64", () => {
  it("encodes a 32-hex trace id as 16 base64 bytes", () => {
    // 000102...0e0f — byte values equal their index.
    const hex = "000102030405060708090a0b0c0d0e0f";
    const decoded = atob(traceIdToBase64(hex));
    expect(decoded.length).toBe(16);
    for (let i = 0; i < 16; i++) {
      expect(decoded.charCodeAt(i)).toBe(i);
    }
  });

  it("round-trips a realistic id", () => {
    const hex = "4bf92f3577b34da6a3ce929d0e0e4736";
    const decoded = atob(traceIdToBase64(hex));
    const back = [...decoded].map((c) => c.charCodeAt(0).toString(16).padStart(2, "0")).join("");
    expect(back).toBe(hex);
  });

  it.each([
    "",
    "short",
    "4bf92f3577b34da6a3ce929d0e0e473",
    "4BF92F3577B34DA6A3CE929D0E0E4736",
    "zzf92f3577b34da6a3ce929d0e0e4736",
  ])("rejects %j", (bad) => {
    expect(traceIdToBase64(bad)).toBe("");
  });
});

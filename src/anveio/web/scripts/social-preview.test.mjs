import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { previewSourceHash } from "./preview-source.mjs";
import preview from "../src/deck/social-preview.json";

describe("deck social preview", () => {
  it("matches the current title slide, brand, fonts and rendering recipe", () => {
    expect(
      preview.sourceHash,
      "Run vp run @guardian/anveio-web#social-preview after title/brand edits",
    ).toBe(previewSourceHash());
  });
  it("ships the content-addressed PNG at the declared social dimensions", () => {
    const png = readFileSync(new URL(`../public${preview.image}`, import.meta.url));
    expect(png.subarray(1, 4).toString()).toBe("PNG");
    expect(png.readUInt32BE(16)).toBe(preview.width);
    expect(png.readUInt32BE(20)).toBe(preview.height);
    expect(createHash("sha256").update(png).digest("hex")).toBe(preview.sha256);
    expect(preview.image).toContain(preview.sha256.slice(0, 16));
  });
});

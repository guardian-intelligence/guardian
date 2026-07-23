import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const publicDirectory = join(dirname(fileURLToPath(import.meta.url)), "../../public");

interface ManifestIcon {
  src: string;
  sizes: string;
  type: string;
  purpose: string;
}

interface WebManifest {
  background_color: string;
  icons: ManifestIcon[];
  theme_color: string;
}

function readPublicFile(path: string): Buffer {
  return readFileSync(join(publicDirectory, path));
}

function readPngHeader(path: string) {
  const png = readPublicFile(path);
  expect(png.subarray(0, 8).toString("hex")).toBe("89504e470d0a1a0a");
  expect(png.subarray(12, 16).toString("ascii")).toBe("IHDR");

  return {
    width: png.readUInt32BE(16),
    height: png.readUInt32BE(20),
    colorType: png.readUInt8(25),
  };
}

describe("platform icons", () => {
  it("gives Apple full-bleed square artwork instead of a pre-rounded icon", () => {
    const favicon = readPublicFile("favicon.svg").toString("utf8");

    expect(favicon).toContain('viewBox="105 106 140 140"');
    expect(favicon).toContain('<rect x="105" y="106" width="140" height="140"');
    expect(favicon).not.toMatch(/\brx=/);

    expect(readPngHeader("apple-touch-icon.png")).toEqual({
      width: 180,
      height: 180,
      colorType: 2,
    });
  });

  it("publishes opaque standard and maskable Android icons", () => {
    const manifest = JSON.parse(
      readPublicFile("manifest.webmanifest").toString("utf8"),
    ) as WebManifest;

    expect(manifest.background_color).toBe("#0a0a0e");
    expect(manifest.theme_color).toBe("#0a0a0e");
    expect(manifest.icons).toEqual([
      {
        src: "/icon-192.png",
        sizes: "192x192",
        type: "image/png",
        purpose: "any",
      },
      {
        src: "/icon-512.png",
        sizes: "512x512",
        type: "image/png",
        purpose: "any",
      },
      {
        src: "/icon-maskable-512.png",
        sizes: "512x512",
        type: "image/png",
        purpose: "maskable",
      },
    ]);

    expect(readPngHeader("icon-192.png")).toEqual({
      width: 192,
      height: 192,
      colorType: 2,
    });
    expect(readPngHeader("icon-512.png")).toEqual({
      width: 512,
      height: 512,
      colorType: 2,
    });
    expect(readPngHeader("icon-maskable-512.png")).toEqual({
      width: 512,
      height: 512,
      colorType: 2,
    });
  });
});

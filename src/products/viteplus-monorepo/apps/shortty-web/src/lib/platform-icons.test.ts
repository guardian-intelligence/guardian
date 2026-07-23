import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { inflateSync } from "node:zlib";
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

function readRgbPngPixels(path: string) {
  const png = readPublicFile(path);
  const { width, height, colorType } = readPngHeader(path);
  expect(png.readUInt8(24)).toBe(8);
  expect(colorType).toBe(2);
  expect(png.readUInt8(28)).toBe(0);

  const imageData: Buffer[] = [];
  for (let offset = 8; offset < png.length; ) {
    const length = png.readUInt32BE(offset);
    const type = png.subarray(offset + 4, offset + 8).toString("ascii");
    if (type === "IDAT") {
      imageData.push(png.subarray(offset + 8, offset + 8 + length));
    }
    offset += length + 12;
  }

  const filtered = inflateSync(Buffer.concat(imageData));
  const bytesPerPixel = 3;
  const stride = width * bytesPerPixel;
  const pixels = Buffer.alloc(height * stride);
  let sourceOffset = 0;

  for (let y = 0; y < height; y += 1) {
    const filter = filtered[sourceOffset] ?? -1;
    sourceOffset += 1;
    for (let x = 0; x < stride; x += 1) {
      const left =
        x >= bytesPerPixel ? (pixels[y * stride + x - bytesPerPixel] ?? 0) : 0;
      const above = y > 0 ? (pixels[(y - 1) * stride + x] ?? 0) : 0;
      const upperLeft =
        y > 0 && x >= bytesPerPixel
          ? (pixels[(y - 1) * stride + x - bytesPerPixel] ?? 0)
          : 0;
      const paethBase = left + above - upperLeft;
      const leftDistance = Math.abs(paethBase - left);
      const aboveDistance = Math.abs(paethBase - above);
      const upperLeftDistance = Math.abs(paethBase - upperLeft);
      const paeth =
        leftDistance <= aboveDistance && leftDistance <= upperLeftDistance
          ? left
          : aboveDistance <= upperLeftDistance
            ? above
            : upperLeft;
      const predictors: Record<number, number> = {
        0: 0,
        1: left,
        2: above,
        3: Math.floor((left + above) / 2),
        4: paeth,
      };
      const predictor = predictors[filter];
      expect(predictor).toBeDefined();
      pixels[y * stride + x] =
        ((filtered[sourceOffset + x] ?? 0) + (predictor ?? 0)) & 0xff;
    }
    sourceOffset += stride;
  }

  return { width, height, pixels };
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

    const { width, height, pixels } = readRgbPngPixels("apple-touch-icon.png");
    const background = "0a0a0e";
    const pixelAt = (x: number, y: number) =>
      pixels.subarray((y * width + x) * 3, (y * width + x + 1) * 3).toString("hex");
    expect([
      pixelAt(0, 0),
      pixelAt(width - 1, 0),
      pixelAt(0, height - 1),
      pixelAt(width - 1, height - 1),
    ]).toEqual([background, background, background, background]);
  });

  it("publishes opaque standard and maskable Android icons", () => {
    const manifest = JSON.parse(
      readPublicFile("manifest.webmanifest").toString("utf8"),
    ) as WebManifest;
    const maskableIcon = readPublicFile("icon-maskable.svg").toString("utf8");

    expect(manifest.background_color).toBe("#0a0a0e");
    expect(manifest.theme_color).toBe("#0a0a0e");
    expect(maskableIcon).toContain('<rect x="105" y="106" width="140" height="140"');
    expect(maskableIcon).toContain("scale(.8)");
    expect(maskableIcon).not.toMatch(/\brx=/);
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

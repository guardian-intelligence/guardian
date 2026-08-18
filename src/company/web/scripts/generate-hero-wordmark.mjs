import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import opentype from "opentype.js";
import { decompress } from "wawoff2";

const fontUrl = new URL("../public/fonts/Geist-Variable.woff2", import.meta.url);
const compressedFont = await readFile(fontUrl);
const decompressedFont = await decompress(compressedFont);
const fontBuffer = decompressedFont.buffer.slice(
  decompressedFont.byteOffset,
  decompressedFont.byteOffset + decompressedFont.byteLength,
);
const font = opentype.parse(fontBuffer);
const glyphPaths = [];

font.forEachGlyph("Guardian", 0, 0, 1000, { kerning: true }, (glyph, x, y, size, options) => {
  glyphPaths.push(glyph.getPath(x, y, size, options));
});

const bounds = glyphPaths.map((path) => path.getBoundingBox());
const minX = Math.min(...bounds.map(({ x1 }) => x1));
const minY = Math.min(...bounds.map(({ y1 }) => y1));
const maxX = Math.max(...bounds.map(({ x2 }) => x2));
const maxY = Math.max(...bounds.map(({ y2 }) => y2));

for (const path of glyphPaths) {
  for (const command of path.commands) {
    for (const coordinate of Object.keys(command)) {
      if (/^x[12]?$/.test(coordinate)) command[coordinate] -= minX;
      if (/^y[12]?$/.test(coordinate)) command[coordinate] -= minY;
    }
  }
}

const wordmark = {
  width: maxX - minX,
  height: maxY - minY,
  glyphs: glyphPaths.map((path) => path.toPathData(2)),
};

process.stdout.write(
  `export const GUARDIAN_HERO_WORDMARK = ${JSON.stringify(wordmark, null, 2)} as const;\n`,
);

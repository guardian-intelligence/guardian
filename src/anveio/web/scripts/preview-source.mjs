import { createHash } from "node:crypto";
import { readFileSync, readdirSync } from "node:fs";

const root = new URL("../", import.meta.url);
// Include the real title, its CSS, fonts and shared brand implementation.
export function previewSourceHash() {
  const paths = [
    "src/deck/Title.tsx",
    "src/deck/Brand.tsx",
    "src/styles/app.css",
    ...readdirSync(new URL("public/deck/fonts/", root))
      .filter((p) => p.endsWith(".woff2"))
      .map((p) => `public/deck/fonts/${p}`),
    ...["components/lockup.tsx", "components/wings.tsx", "tokens.ts"].map(
      (p) => `../../shared/ts/brand/src/${p}`,
    ),
  ].sort();
  const hash = createHash("sha256");
  for (const path of paths) hash.update(path).update(readFileSync(new URL(path, root)));
  // Rendering recipe changes also require regeneration.
  hash.update(readFileSync(new URL("scripts/social-preview.mjs", root)));
  return hash.digest("hex");
}

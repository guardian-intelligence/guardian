// Turns the prerendered TanStack Start output into the single static HTML
// file the aisucks Go API binary embeds and serves.
//
// - Strips every <script> and modulepreload hint: the page must work with no
//   JavaScript (charter value 5), and aisucks' Go tests enforce it.
// - Inlines the stylesheet: the page is one response, TTI == TTFB.
//
// Run via `pnpm generate` (vp build && node scripts/emit-static.mjs) and
// commit the result, the same way routeTree.gen.ts is committed.
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const outDir = resolve(here, "../.output/public");

let html = readFileSync(resolve(outDir, "index.html"), "utf8");

html = html.replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi, "");
html = html.replace(/<link\b[^>]*rel="modulepreload"[^>]*>/gi, "");
html = html.replace(/<link\b[^>]*rel="stylesheet"[^>]*href="([^"]+)"[^>]*>/gi, (_m, href) => {
  const css = readFileSync(resolve(outDir, `.${href}`), "utf8").trim();
  return `<style>${css}</style>`;
});

if (/<script/i.test(html)) {
  throw new Error("emit-static: a <script> tag survived stripping");
}

const dest = resolve(here, "../../services/api/web/index.html");
mkdirSync(dirname(dest), { recursive: true });
writeFileSync(dest, html);
console.log(`wrote ${dest} (${html.length} bytes)`);

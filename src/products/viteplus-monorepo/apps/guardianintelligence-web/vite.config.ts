import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import viteReact from "@vitejs/plugin-react";
import matter from "gray-matter";
import { marked } from "marked";
import { nitro } from "nitro/vite";
import { defineConfig } from "vite-plus";
// Local bundled Nitro hooks. Keeping them as .mjs files avoids introducing a
// separate plugin package into the vp build module graph.
import { rewriteCjsRequireOnCompiled } from "./rewrite-cjs-require.mjs";
// Per-word ink variation for the letters treatment. Pure hash of
// (slug, word index) — deterministic, so rebuilds of the same content keep
// producing the same bytes (the image digest pin depends on that).
import { inkWrapHtml } from "./src/features/letters/ink";

const observabilityPlugin = fileURLToPath(new URL("./observability-plugin.mjs", import.meta.url));

// Letters markdown loader. Each src/content/letters/*.md becomes a JS module
// exporting { frontmatter, html, leadHtml, continuationHtml } parsed at build
// time. Keeps the markdown parser out of the browser bundle entirely — the
// runtime only sees the pre-rendered HTML, so client navigation between
// letters is a static asset hop with no parse cost.
const LETTERS_MD = /\/src\/content\/letters\/[^/]+\.md$/;
const lettersMarkdown = {
  name: "company:letters-markdown",
  enforce: "pre" as const,
  load(id: string) {
    if (!LETTERS_MD.test(id)) return null;
    const raw = readFileSync(id, "utf8");
    const { data, content } = matter(raw);
    // gray-matter parses unquoted YAML dates (publishedAt: 2026-04-08) into
    // JS Dates. JSON.stringify would then emit a full ISO datetime, which
    // breaks the YYYY-MM-DD contract Letter consumers expect. Walk the
    // frontmatter and flatten any Date to a date-only string before serialise.
    const normalised: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(data)) {
      normalised[key] = value instanceof Date ? value.toISOString().slice(0, 10) : value;
    }
    const tokens = marked.lexer(content);
    const flowTokens = tokens.filter((token) => token.type !== "space");
    const [leadToken, ...continuationTokens] = flowTokens;
    // Every rendered word of the letter wears its own ink (see
    // features/letters/ink.ts). Word indices count from 0 per fragment; the
    // index excerpt re-counts the lead's words from 0 at render time, so both
    // sides of the view transition agree on each word's ink. The full `html`
    // stays unwrapped: it never renders (LetterBody reads lead/continuation;
    // the excerpt fallback strips tags), and the letter already ships in the
    // payload once as markup and once as loader data — no third inked copy.
    const slug = typeof normalised.slug === "string" ? normalised.slug : "";
    const html = marked.parser(tokens);
    const leadHtml = leadToken ? inkWrapHtml(marked.parser([leadToken]), slug) : "";
    const continuationHtml =
      continuationTokens.length > 0
        ? inkWrapHtml(marked.parser(continuationTokens), slug)
        : "";
    return `export default ${JSON.stringify({
      frontmatter: normalised,
      html,
      leadHtml,
      continuationHtml,
    })};`;
  },
};

// TanStack Start's generated route manifest embeds absolute filePath entries
// for the route sources. Those paths differ per machine (workstation vs CI
// runner) and feed the manifest chunk's content hash, breaking reproducible
// image digests — and they point at .tsx sources that don't exist in the
// container anyway. Relativize them to the package root before bundling.
const stripRouteManifestPaths = {
  name: "company:strip-route-manifest-paths",
  enforce: "post" as const,
  transform(code: string, id: string) {
    if (!id.includes("tanstack-start-manifest")) return null;
    return code.replace(/("filePath":\s*")[^"]*\/src\/products\/viteplus-monorepo\/apps\/guardianintelligence-web\//g, "$1");
  },
};

export default defineConfig({
  build: {
    rollupOptions: {
      // Rolldown's debug //#region comments embed relative paths into the
      // Bazel output base, which differs per machine and lands in the
      // content-hashed chunk names — breaking reproducible image digests.
      // The company-site-image workflow enforces pin == built digest, which
      // requires the build to be a pure function of the sources.
      experimental: { attachDebugInfo: "none" },
    },
  },
  server: {
    host: "127.0.0.1",
    port: 4252,
    strictPort: true,
  },
  resolve: {
    tsconfigPaths: true,
  },
  plugins: [
    lettersMarkdown,
    stripRouteManifestPaths,
    tailwindcss(),
    tanstackStart({ srcDirectory: "src" }),
    viteReact(),
    nitro({
      plugins: [observabilityPlugin],
      hooks: { compiled: rewriteCjsRequireOnCompiled },
    }),
  ],
  test: {
    exclude: ["**/node_modules/**"],
    passWithNoTests: true,
  },
});

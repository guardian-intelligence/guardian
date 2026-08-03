import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import viteReact from "@vitejs/plugin-react";
import { nitro } from "nitro/vite";
import { defineConfig } from "vite-plus";
// Local bundled Nitro hooks. Keeping them as .mjs files avoids introducing a
// separate plugin package into the vp build module graph.
import { rewriteCjsRequireOnCompiled } from "./rewrite-cjs-require.mjs";

const observabilityPlugin = fileURLToPath(new URL("./observability-plugin.mjs", import.meta.url));

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
    return code.replace(
      /("filePath":\s*")[^"]*\/src\/products\/viteplus-monorepo\/apps\/guardianintelligence-web\//g,
      "$1",
    );
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

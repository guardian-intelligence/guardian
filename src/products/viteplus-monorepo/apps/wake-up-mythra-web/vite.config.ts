import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import viteReact from "@vitejs/plugin-react";
import { nitro } from "nitro/vite";
import { defineConfig } from "vite-plus";

// TanStack Start's generated route manifest embeds absolute filePath entries
// for the route sources. Those paths differ per machine (workstation vs CI
// runner) and feed the manifest chunk's content hash, breaking reproducible
// image digests — and they point at .tsx sources that don't exist in the
// container anyway. Relativize them to the package root before bundling.
const stripRouteManifestPaths = {
  name: "wake-up-mythra:strip-route-manifest-paths",
  enforce: "post" as const,
  transform(code: string, id: string) {
    if (!id.includes("tanstack-start-manifest")) return null;
    return code.replace(/((?:"filePath"|filePath):\s*")[^"]*\/apps\/wake-up-mythra-web\//g, "$1");
  },
};

export default defineConfig({
  build: {
    // The apex Ingress path-routes /assets/ to the game service (streamed
    // skin SVGs, content-addressed); the web bundle's chunks must live
    // elsewhere or they'd be routed away from this server.
    assetsDir: "static",
    rollupOptions: {
      // Rolldown's debug //#region comments embed relative paths into the
      // Bazel output base, which differs per machine and lands in the
      // content-hashed chunk names — breaking reproducible image digests.
      // The image workflow enforces pin == built digest, which requires the
      // build to be a pure function of the sources.
      experimental: { attachDebugInfo: "none" },
    },
  },
  server: {
    host: "127.0.0.1",
    port: 4254,
    strictPort: true,
    // File-suffixed paths (.wasm, .svg) traverse vite's middleware in dev;
    // extension-less paths go to nitro (see devProxy below). Both proxies
    // point at a locally running presenced, mirroring the prod Ingress split.
    proxy: {
      "/assets": "http://127.0.0.1:9634",
      "/behavior": "http://127.0.0.1:9634",
    },
  },
  resolve: {
    tsconfigPaths: true,
  },
  plugins: [
    stripRouteManifestPaths,
    tanstackStart({ srcDirectory: "src" }),
    viteReact(),
    // Local loop: run presenced locally (HTTP_PORT 9634) and the dev server
    // forwards the game-service paths, mirroring the prod Ingress split.
    // Nitro's dev server fronts every request, so the forwarding must be its
    // devProxy — vite's server.proxy never sees these paths.
    nitro({
      devProxy: {
        "/wt-info": "http://127.0.0.1:9634",
        "/assets/**": "http://127.0.0.1:9634",
        "/behavior/**": "http://127.0.0.1:9634",
      },
    }),
  ],
  test: {
    exclude: ["**/node_modules/**"],
    passWithNoTests: true,
  },
});

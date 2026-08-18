import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import { fileURLToPath } from "node:url";

import viteReact from "@vitejs/plugin-react";
import { nitro } from "nitro/vite";
import { defineConfig } from "vite-plus";

// TanStack Start's generated route manifest embeds absolute filePath entries
// for the route sources. Those paths differ per machine (workstation vs CI
// runner) and feed the manifest chunk's content hash, breaking reproducible
// image digests — and they point at .tsx sources that don't exist in the
// container anyway. Relativize them to the package root before bundling.
// Derived from this file's own location rather than written out: a hardcoded
// package path stops matching the day the package moves, and the failure is
// silent here -- the paths simply stay absolute and the digest stops being
// reproducible. The build-time guard below is what caught it last time.
const packageRoot = fileURLToPath(new URL(".", import.meta.url));

const stripRouteManifestPaths = {
  name: "wake-up-mythra:strip-route-manifest-paths",
  enforce: "post" as const,
  transform(code: string, id: string) {
    if (!id.includes("tanstack-start-manifest")) return null;
    return code.replaceAll(packageRoot, "");
  },
};

const gatewayOrigin = `http://127.0.0.1:${process.env["WUM_DEV_GATEWAY_HTTP_PORT"] ?? "9634"}`;

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
    proxy: {
      "/assets": gatewayOrigin,
      "/behavior": gatewayOrigin,
    },
  },
  resolve: {
    tsconfigPaths: true,
  },
  plugins: [
    stripRouteManifestPaths,
    tanstackStart({ srcDirectory: "src" }),
    viteReact(),
    nitro({
      devProxy: {
        "/api/events/**": `http://127.0.0.1:${process.env["WUM_DEV_INGEST_PORT"] ?? "9636"}`,
        "/wt-info": gatewayOrigin,
        "/session": gatewayOrigin,
        "/terrain/**": gatewayOrigin,
        "/assets/**": gatewayOrigin,
        "/behavior/**": gatewayOrigin,
      },
    }),
  ],
  test: {
    exclude: ["**/node_modules/**"],
    passWithNoTests: true,
  },
});

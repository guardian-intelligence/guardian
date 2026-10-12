import { fileURLToPath } from "node:url";

import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import viteReact from "@vitejs/plugin-react";
import { nitro } from "nitro/vite";
import { defineConfig } from "vite-plus";

// TanStack Start's generated route manifest embeds absolute filePath entries.
// Relativize them so the image digest is a pure function of the sources; the
// BUILD.bazel genrule guards that this stays effective.
const packageRoot = fileURLToPath(new URL(".", import.meta.url));

const stripRouteManifestPaths = {
  name: "anveio:strip-route-manifest-paths",
  enforce: "post" as const,
  transform(code: string, id: string) {
    if (!id.includes("tanstack-start-manifest")) return null;
    return code.replaceAll(packageRoot, "");
  },
};

// The investor deck is unlisted: reachable by link, never indexed.
const unlisted = { headers: { "x-robots-tag": "noindex, nofollow, noarchive" } } as const;

// This app is anveio.com: the company pages at /, /contact and /privacy and the
// deck at /deck. The ingress sends everything here except /shovon/**, /oauth/**
// and /cal (anveio-cal, Shovon's contact card). Every page shares the built
// chunks under /deck/assets and the fonts and images under public/deck.
export default defineConfig(({ mode }) => ({
  build: {
    assetsDir: "deck/assets",
    rollupOptions: {
      // Debug region comments embed machine-specific paths into hashed chunks.
      experimental: { attachDebugInfo: "none" },
    },
  },
  server: {
    host: "127.0.0.1",
    port: 4254,
    strictPort: true,
  },
  resolve: {
    tsconfigPaths: true,
  },
  // Unit tests inspect source artifacts and do not need SSR worker servers.
  plugins:
    mode === "test"
      ? []
      : [
          stripRouteManifestPaths,
          tanstackStart({ srcDirectory: "src" }),
          viteReact(),
          nitro({
            routeRules: {
              "/deck": unlisted,
              "/deck/**": unlisted,
              // The policy's address when anveio-cal served it as a file.
              "/privacy.html": { redirect: { to: "/privacy", status: 301 } },
            },
          }),
        ],
  test: {
    exclude: ["**/node_modules/**"],
    passWithNoTests: true,
  },
}));

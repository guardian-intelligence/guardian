import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";

const packageDirectory = fileURLToPath(new URL(".", import.meta.url));
const entry = resolve(packageDirectory, "src/action-main.ts");
const actionDirectory = resolve(packageDirectory, "../../../../../actions/checkout/dist");

export default defineConfig({
  build: {
    emptyOutDir: true,
    minify: "oxc",
    outDir: process.env.POSTFLIGHT_ACTION_OUT_DIR ?? actionDirectory,
    rollupOptions: {
      // Rolldown otherwise embeds machine-specific dependency paths in
      // //#region comments, making the checked-in action bundle non-reproducible.
      experimental: { attachDebugInfo: "none" },
      output: {
        postBanner: [
          "// GENERATED FILE. DO NOT EDIT.",
          "// Source: src/products/viteplus-monorepo/packages/postflight-checkout/src/action-main.ts",
          "// Regenerate: cd src/products/viteplus-monorepo && vp run @guardian/postflight-checkout#build",
        ].join("\n"),
        entryFileNames: "index.js",
        format: "cjs",
        codeSplitting: false,
      },
    },
    sourcemap: false,
    ssr: entry,
    target: "node24",
  },
  ssr: {
    noExternal: true,
  },
});

import tailwindcss from "@tailwindcss/vite";
import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import viteReact from "@vitejs/plugin-react";
import { nitro } from "nitro/vite";
import { defineConfig } from "vite-plus";

export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: 4260,
    strictPort: true,
  },
  resolve: {
    tsconfigPaths: true,
  },
  plugins: [
    tailwindcss(),
    // The site ships as build-time-prerendered static HTML embedded in the Go
    // binary (scripts/emit-static.mjs); no Node server runs in production.
    // Charter value 5: server-rendered, no required JavaScript.
    tanstackStart({
      srcDirectory: "src",
      prerender: { enabled: true, crawlLinks: false },
    }),
    viteReact(),
    nitro(),
  ],
  test: {
    exclude: ["**/node_modules/**"],
    passWithNoTests: true,
  },
});

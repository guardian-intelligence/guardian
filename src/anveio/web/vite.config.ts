import { execFile } from "node:child_process";
import { renameSync, writeFileSync } from "node:fs";
import { request } from "node:http";
import { fileURLToPath } from "node:url";

import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import viteReact from "@vitejs/plugin-react";
import { nitro } from "nitro/vite";
import { defineConfig } from "vite-plus";

import { parseScale } from "./src/home/typeScale.ts";

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

// The investor deck and the /design editor are unlisted: reachable by link,
// never indexed.
const unlisted = { headers: { "x-robots-tag": "noindex, nofollow, noarchive" } } as const;

// The built chunks stay under /deck/assets, which reaches this app under any
// anveio.com ingress. The home page is served at / with its media in
// public/home; the contact card (anveio-cal) lives at /agent.
//
// In `vp dev`, /agent and the card's root /oauth/ page are proxied to
// anveio-cal's dev server (`vp run dev:cal`, :4255), so one origin previews
// the whole site. It's a plugin middleware rather than server.proxy because
// the SSR handler answers every path before Vite's own proxy runs.
const calDev = {
  name: "anveio:cal-dev-proxy",
  apply: "serve" as const,
  enforce: "pre" as const,
  configureServer(server: { middlewares: { use: (fn: Middleware) => void } }) {
    server.middlewares.use((req, res, next) => {
      if (!/^\/(agent|oauth)(\/|$|\?)/.test(req.url ?? "")) return next();
      // Vite only answers under the base with its trailing slash.
      if (/^\/agent($|\?)/.test(req.url ?? "")) {
        res.writeHead(302, { location: req.url!.replace("/agent", "/agent/") });
        return res.end();
      }
      const upstream = request(
        { host: "127.0.0.1", port: 4255, path: req.url, method: req.method, headers: req.headers },
        (r) => {
          res.writeHead(r.statusCode ?? 502, r.headers);
          r.pipe(res);
        },
      );
      upstream.on("error", () => {
        res.writeHead(502, { "content-type": "text/plain" });
        res.end("anveio-cal's dev server isn't running: vp run dev:cal\n");
      });
      req.pipe(upstream);
    });
  },
};
// /design's Save, in `vp dev` only: writes the edited type scale back to
// src/home/type-scale.json (validated, then formatted like the rest of the
// repo), which the home page imports. JSON-only and same-origin, so another
// site can't post to it from the browser.
const scaleFile = fileURLToPath(new URL("src/home/type-scale.json", import.meta.url));
const repoRoot = fileURLToPath(new URL("../../../", import.meta.url));
const designSave = {
  name: "anveio:design-save",
  apply: "serve" as const,
  enforce: "pre" as const,
  configureServer(server: { middlewares: { use: (fn: Middleware) => void } }) {
    server.middlewares.use((req, res, next) => {
      if (req.url !== "/__design/type-scale") return next();
      const reply = (status: number, text: string) => {
        res.writeHead(status, { "content-type": "text/plain" });
        res.end(text);
      };
      const origin = req.headers.origin;
      if (
        req.method !== "POST" ||
        !req.headers["content-type"]?.startsWith("application/json") ||
        (origin !== undefined && origin !== `http://${req.headers.host}`)
      ) {
        return reply(400, "POST the type scale as JSON from /design\n");
      }
      let body = "";
      req.setEncoding("utf8");
      req.on("data", (chunk: string) => (body += chunk));
      req.on("end", () => {
        let json: string;
        try {
          json = `${JSON.stringify(parseScale(JSON.parse(body)), null, 2)}\n`;
        } catch (error) {
          return reply(422, error instanceof Error ? error.message : "invalid type scale");
        }
        writeFileSync(`${scaleFile}.tmp`, json);
        renameSync(`${scaleFile}.tmp`, scaleFile);
        execFile(
          process.execPath,
          [
            fileURLToPath(new URL("node_modules/vite-plus/bin/vp", import.meta.url)),
            "fmt",
            scaleFile,
          ],
          { cwd: repoRoot },
          () => reply(204, ""),
        );
      });
    });
  },
};
type Middleware = (
  req: import("node:http").IncomingMessage,
  res: import("node:http").ServerResponse,
  next: () => void,
) => void;
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
          calDev,
          designSave,
          stripRouteManifestPaths,
          tanstackStart({ srcDirectory: "src" }),
          viteReact(),
          nitro({
            routeRules: {
              "/deck": unlisted,
              "/deck/**": unlisted,
              "/design": unlisted,
            },
          }),
        ],
  test: {
    exclude: ["**/node_modules/**"],
    passWithNoTests: true,
  },
}));

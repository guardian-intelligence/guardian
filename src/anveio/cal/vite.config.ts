import { writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

import viteReact from "@vitejs/plugin-react";
import * as v from "valibot";
import { defineConfig, type Plugin } from "vite-plus";

import { TuningSchema, renderTuningModule } from "./src/tuning-schema.ts";

const tuningFile = fileURLToPath(new URL("./src/tuning.ts", import.meta.url));

// Dev-only: the studio's "Save as defaults" posts its tuning here and we
// rewrite src/tuning.ts, which HMR then pushes straight back into the page.
const studioSave: Plugin = {
  name: "anveio-cal:studio-save",
  apply: "serve",
  configureServer(server) {
    server.middlewares.use("/__studio/tuning", (req, res) => {
      if (req.method !== "POST") {
        res.statusCode = 405;
        res.end();
        return;
      }
      let body = "";
      req.on("data", (chunk: Buffer) => {
        body += chunk.toString("utf8");
      });
      req.on("end", () => {
        let json: unknown;
        try {
          json = JSON.parse(body);
        } catch {
          json = undefined;
        }
        const parsed = v.safeParse(TuningSchema, json);
        if (!parsed.success) {
          res.statusCode = 400;
          res.end(v.summarize(parsed.issues));
          return;
        }
        writeFile(tuningFile, renderTuningModule(parsed.output)).then(
          () => {
            res.statusCode = 204;
            res.end();
          },
          (err: unknown) => {
            res.statusCode = 500;
            res.end(String(err));
          },
        );
      });
    });
  },
};

export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: 4255,
    strictPort: true,
  },
  plugins: [studioSave, viteReact()],
});

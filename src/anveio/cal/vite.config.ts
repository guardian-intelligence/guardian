import viteReact from "@vitejs/plugin-react";
import type { Connect, Plugin } from "vite";
import { defineConfig } from "vite-plus";

// The app lives at anveio.com/agent, but Google's sign-in hands back to
// /oauth/google.html at the site root: that redirect URI is registered with
// the anveio-cal OAuth client, so it keeps its URL. The built page sits in
// dist/oauth/ and server/index.mjs serves it at the root; this plugin does
// the same for `vp dev` and `vp preview`, which otherwise only answer under
// the base.
const BASE = "/agent/";
const rootOAuth = (): Plugin => {
  const rewrite: Connect.NextHandleFunction = (req, _res, next) => {
    if (req.url?.startsWith("/oauth/")) req.url = BASE.slice(0, -1) + req.url;
    next();
  };
  return {
    name: "anveio-cal:root-oauth",
    configureServer: (server) => void server.middlewares.use(rewrite),
    configurePreviewServer: (server) => void server.middlewares.use(rewrite),
  };
};

export default defineConfig({
  base: BASE,
  server: {
    host: "127.0.0.1",
    port: 4255,
    strictPort: true,
    // HMR connects straight to this server, also when anveio-web's dev
    // server proxies /agent here.
    hmr: { clientPort: 4255 },
  },
  plugins: [rootOAuth(), viteReact()],
});

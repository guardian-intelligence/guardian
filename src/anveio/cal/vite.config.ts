import viteReact from "@vitejs/plugin-react";
import { defineConfig } from "vite-plus";

export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: 4255,
    strictPort: true,
  },
  plugins: [viteReact()],
});

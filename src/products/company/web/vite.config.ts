import { resolve } from "node:path";
import { defineConfig } from "vite-plus";
import { writeGoSource, writeStaticSite } from "./scripts/site.mjs";

function companyStaticSite() {
  let outDir = "";
  return {
    name: "guardian-company-static-site",
    configResolved(config) {
      outDir = config.build.outDir;
    },
    writeBundle() {
      const count = writeStaticSite(outDir);
      if (process.env.GUARDIAN_COMPANY_GO_OUT) {
        writeGoSource(process.env.GUARDIAN_COMPANY_GO_OUT);
      }
      this.info(`generated ${count} company site assets`);
    },
  };
}

export default defineConfig({
  build: {
    rollupOptions: {
      input: resolve(import.meta.dirname, "src/entry.ts"),
    },
  },
  plugins: [companyStaticSite()],
});

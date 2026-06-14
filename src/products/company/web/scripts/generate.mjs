import { resolve } from "node:path";
import { writeGoSource, writeStaticSite } from "./site.mjs";

const outDir = resolve(process.env.GUARDIAN_COMPANY_STATIC_OUT ?? "dist");
const goOut = process.env.GUARDIAN_COMPANY_GO_OUT;
const count = writeStaticSite(outDir);
if (goOut) {
  writeGoSource(resolve(goOut));
}
console.error(`generated ${count} company site assets`);

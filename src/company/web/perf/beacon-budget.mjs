// Beacon budget assertions, run against a completed `vp build`:
//   node perf/beacon-budget.mjs [.output dir]
// 1. the beacon chunk exists as its own lazy chunk
// 2. gzip size <= 5 KiB
// 3. no other asset references it via a STATIC import (only import(...) —
//    a static edge would pull it into the entry graph and modulepreload it)
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { gzipSync } from "node:zlib";

const out = process.argv[2] ?? new URL("../.output", import.meta.url).pathname;
const assetsDir = join(out, "public", "assets");
const assets = readdirSync(assetsDir).filter((f) => f.endsWith(".js"));

const beacon = assets.filter((f) => /^beacon-[A-Za-z0-9_-]+\.js$/.test(f));
if (beacon.length !== 1) {
  console.error(`FAIL: expected exactly one beacon chunk, found: ${beacon.join(", ") || "none"}`);
  process.exit(1);
}
const name = beacon[0];
const raw = readFileSync(join(assetsDir, name));
const gz = gzipSync(raw, { level: 9 }).length;
if (gz > 5 * 1024) {
  console.error(`FAIL: beacon chunk ${name} is ${gz} bytes gzipped (budget 5120)`);
  process.exit(1);
}

let staticRefs = 0;
for (const f of assets) {
  if (f === name) continue;
  const src = readFileSync(join(assetsDir, f), "utf8");
  let idx = 0;
  while ((idx = src.indexOf(name, idx)) !== -1) {
    // Walk back past quote + separator to see if this is import("...").
    const before = src.slice(Math.max(0, idx - 40), idx);
    if (!/import\(\s*["'`][^"'`]*$/.test(before)) {
      console.error(`FAIL: ${f} references ${name} outside a dynamic import()`);
      staticRefs++;
    }
    idx += name.length;
  }
}
if (staticRefs > 0) process.exit(1);

console.log(
  `OK: ${name} raw=${statSync(join(assetsDir, name)).size}B gzip=${gz}B (budget 5120B), dynamic-import only`,
);

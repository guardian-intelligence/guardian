import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { parseArgs } from "node:util";
import { createCaptureSession, type EngineName } from "../src/capture.ts";
import { parseFormFactors } from "../src/form-factors.ts";

const USAGE = `Deterministic visual capture for agent-driven iteration.

Usage:
  vp exec tsx bin/capture-cli.ts --url <target> --out <dir> [options]

Options:
  --url <url>            Target page (required)
  --out <dir>            Output directory for PNGs + manifest.json (required)
  --form-factor <list>   Comma-separated form factors, or "all" (default: all)
  --seek <list>          Comma-separated freeze timestamps in ms (default: 0)
  --engine <name>        chromium | firefox | webkit (default: chromium)
  --reduced-motion       Capture with prefers-reduced-motion: reduce
  --full-page            Capture the full scroll height, not just the viewport
  --seed <n>             Seed for the page's Math.random override (default: 1)
  --selector <css>       Clip the capture to one element
  --wait-selector <css>  Wait for this element to be visible before seeking
  --help                 Show this message

Each run writes <formFactor>__<engine>__seek-<t>ms[__fullpage].png files plus
a manifest.json describing every shot (viewport, scale factor, seed, sha256).
Byte-identical output across runs at the same flags is the contract; a
mismatch is a harness bug.`;

function parseSeekList(spec: string): number[] {
  return spec.split(",").map((part) => {
    const value = Number(part.trim());
    if (!Number.isInteger(value) || value < 0) {
      throw new Error(`invalid --seek value ${JSON.stringify(part.trim())}`);
    }
    return value;
  });
}

async function waitReachable(url: string, budgetMs = 30_000): Promise<void> {
  const healthz = new URL("/healthz", url).toString();
  const deadline = Date.now() + budgetMs;
  let lastError = "";
  while (Date.now() < deadline) {
    for (const probe of [healthz, url]) {
      try {
        const response = await fetch(probe, { signal: AbortSignal.timeout(3_000) });
        if (response.ok) return;
        lastError = `${probe} -> HTTP ${response.status}`;
      } catch (error) {
        lastError = `${probe} -> ${error instanceof Error ? error.message : String(error)}`;
      }
    }
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error(`target unreachable after ${budgetMs}ms: ${lastError}`);
}

async function main(): Promise<number> {
  const { values } = parseArgs({
    options: {
      url: { type: "string" },
      out: { type: "string" },
      "form-factor": { type: "string", default: "all" },
      seek: { type: "string", default: "0" },
      engine: { type: "string", default: "chromium" },
      "reduced-motion": { type: "boolean", default: false },
      "full-page": { type: "boolean", default: false },
      seed: { type: "string", default: "1" },
      selector: { type: "string" },
      "wait-selector": { type: "string" },
      help: { type: "boolean", default: false },
    },
  });

  if (values.help) {
    process.stdout.write(`${USAGE}\n`);
    return 0;
  }
  if (!values.url || !values.out) {
    console.error("--url and --out are required (see --help)");
    return 2;
  }
  const engine = values.engine as EngineName;
  if (!["chromium", "firefox", "webkit"].includes(engine)) {
    console.error(`invalid --engine ${JSON.stringify(values.engine)}`);
    return 2;
  }
  const seed = Number(values.seed);
  if (!Number.isInteger(seed)) {
    console.error(`invalid --seed ${JSON.stringify(values.seed)}`);
    return 2;
  }

  const formFactors = parseFormFactors(values["form-factor"]);
  const seeks = parseSeekList(values.seek);
  const reducedMotion = values["reduced-motion"] ? "reduce" : "no-preference";

  await waitReachable(values.url);
  await mkdir(values.out, { recursive: true });

  const session = await createCaptureSession(engine);
  const manifest: Record<string, unknown>[] = [];
  try {
    for (const formFactor of formFactors) {
      for (const seekMs of seeks) {
        const result = await session.capture({
          url: values.url,
          formFactor,
          seekMs,
          seed,
          reducedMotion,
          fullPage: values["full-page"],
          clipSelector: values.selector,
          waitSelector: values["wait-selector"],
        });
        const name = `${formFactor.name}__${engine}__seek-${seekMs}ms${values["full-page"] ? "__fullpage" : ""}.png`;
        const path = join(values.out, name);
        await writeFile(path, result.png);
        manifest.push({
          file: name,
          url: values.url,
          formFactor: formFactor.name,
          viewport: { width: formFactor.width, height: formFactor.height },
          deviceScaleFactor: formFactor.deviceScaleFactor,
          userAgent: formFactor.userAgent,
          engine,
          seekMs,
          seed,
          reducedMotion,
          fullPage: values["full-page"],
          clipSelector: values.selector,
          width: result.width,
          height: result.height,
          cssAnimationsFrozen: result.cssAnimationsFrozen,
          sha256: result.sha256,
        });
        process.stdout.write(
          `wrote ${path} (${result.width}x${result.height}, sha256 ${result.sha256.slice(0, 12)})\n`,
        );
      }
    }
  } finally {
    await session.close();
  }

  await writeFile(join(values.out, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  process.stdout.write(`wrote ${join(values.out, "manifest.json")} (${manifest.length} shots)\n`);
  return 0;
}

process.exitCode = await main();

import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { sanitizeHar } from "./har-sanitize.ts";
import { registryFromEnv } from "./redact.ts";

// Global teardown: the egress gate for on-disk artifacts. Runs after every
// browser context has closed (HAR files flush on context close), so by the
// time anything could read or ship an artifact, it has already been
// sanitized in place.
export default function sanitizeArtifacts(): void {
  const outputDir = process.env.CANARY_OUTPUT_DIR ?? "/tmp/canary-journeys-output";
  const registry = registryFromEnv(process.env);
  let entries: string[];
  try {
    entries = readdirSync(outputDir, { recursive: true }) as string[];
  } catch {
    return;
  }
  for (const entry of entries) {
    if (!entry.endsWith(".har")) {
      continue;
    }
    const path = join(outputDir, entry);
    const har = JSON.parse(readFileSync(path, "utf8")) as unknown;
    writeFileSync(path, JSON.stringify(sanitizeHar(har, registry)));
    process.stdout.write(
      `${registry.scrub(JSON.stringify({ event: "har-sanitized", file: entry }))}\n`,
    );
  }
}

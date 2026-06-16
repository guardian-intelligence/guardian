import { mkdtemp, mkdir, readFile, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { execFile } from "node:child_process";
import { performance } from "node:perf_hooks";
import { promisify } from "node:util";

import { sdkPackageName } from "./types.js";

const execFileAsync = promisify(execFile);
const operation = "/guardian.products.aisucks.v1.AisucksService/Health";

type GateTrack = "nightly" | "rc";

type GateConfig = {
  readonly track: GateTrack;
  readonly fromChannel: string;
  readonly toChannel: string;
  readonly endpoint: string;
  readonly iterations: number;
  readonly concurrency: number;
  readonly maxP95LatencyMs: number;
  readonly minTps: number;
  readonly maxTarballBytes: number;
  readonly maxUnpackedBytes: number;
  readonly requiredCapability: string;
  readonly outputDir: string | undefined;
  readonly node: string;
  readonly npm: string;
};

type NpmView = {
  readonly name: string;
  readonly version: string;
  readonly dist: {
    readonly integrity: string;
    readonly unpackedSize: number;
    readonly tarball: string;
  };
};

type NpmPackEntry = {
  readonly filename: string;
  readonly integrity: string;
  readonly size: number;
  readonly unpackedSize?: number;
};

type HealthFunction = (options?: {
  readonly baseUrl?: string;
  readonly fetch?: typeof globalThis.fetch;
}) => Promise<{
  readonly status?: string;
  readonly service?: string;
  readonly version?: string;
  readonly capabilities?: readonly string[];
}>;

type SyntheticResult = {
  readonly schemaVersion: "guardian.synthetic-result.v1";
  readonly package: string;
  readonly version: string;
  readonly sourceChannel: string;
  readonly endpointUrl: string;
  readonly operation: string;
  readonly status: "pass" | "fail";
  readonly startedAt: string;
  readonly endedAt: string;
  readonly requestCount: number;
  readonly successCount: number;
  readonly failureCount: number;
  readonly latencyMs: {
    readonly min: number;
    readonly p50: number;
    readonly p95: number;
    readonly max: number;
  };
  readonly observedTps: number;
  readonly packageBytes: {
    readonly tarball: number;
    readonly unpacked: number;
  };
  readonly capabilitiesObserved: readonly string[];
  readonly failures: readonly string[];
};

type GateCheck = {
  readonly name: string;
  readonly observed: number | string;
  readonly threshold: string;
  readonly passed: boolean;
};

type GateResult = {
  readonly schemaVersion: "guardian.gate-result.v1";
  readonly product: "aisucks";
  readonly track: GateTrack;
  readonly fromChannel: string;
  readonly toChannel: string;
  readonly decision: "pass" | "fail";
  readonly package: string;
  readonly version: string;
  readonly endpointUrl: string;
  readonly operation: string;
  readonly checkedAt: string;
  readonly checks: readonly GateCheck[];
  readonly metrics: {
    readonly latencyMs: SyntheticResult["latencyMs"];
    readonly observedTps: number;
    readonly requestCount: number;
    readonly successCount: number;
    readonly tarballBytes: number;
    readonly unpackedBytes: number;
  };
  readonly syntheticResultPath: string;
};

const config = parseGateConfig(process.argv.slice(2));
const outputDir = config.outputDir ?? (await mkdtemp(path.join(tmpdir(), "guardian-aisucks-gate-")));
await mkdir(outputDir, { recursive: true });

const packageSpec = `${sdkPackageName}@${config.fromChannel}`;
const npmView = await npmViewPackage(config, packageSpec);
const packDir = path.join(outputDir, "package");
const installDir = path.join(outputDir, "install");
await mkdir(packDir, { recursive: true });
await mkdir(installDir, { recursive: true });
const packEntry = await npmPack(config, packageSpec, packDir);
const tarballPath = path.join(packDir, packEntry.filename);
const tarballStat = await stat(tarballPath);
await npmInstall(config, tarballPath, installDir);

const health = await loadPublishedHealth(installDir);
const synthetic = await runSynthetic(config, npmView, packEntry, tarballStat.size, health);
const syntheticPath = path.join(outputDir, "synthetic-result.v1.json");
await writeJson(syntheticPath, synthetic);

const gate = gateResult(config, npmView, synthetic, syntheticPath);
const gatePath = path.join(outputDir, "gate-result.v1.json");
await writeJson(gatePath, gate);
await writeFile(path.join(outputDir, "gate-summary.md"), renderSummary(gate), "utf8");

process.stdout.write(`${JSON.stringify({ status: gate.decision, outputDir, gatePath }, null, 2)}\n`);
if (gate.decision !== "pass") {
  process.exitCode = 1;
}

function parseGateConfig(args: readonly string[]): GateConfig {
  let track: GateTrack = "nightly";
  let fromChannel = "edge";
  let toChannel = "nightly";
  let endpoint = "https://gamma.aisucks.app";
  let iterations: number | undefined;
  let concurrency: number | undefined;
  let maxP95LatencyMs: number | undefined;
  let minTps: number | undefined;
  let maxTarballBytes = 250_000;
  let maxUnpackedBytes = 900_000;
  let requiredCapability = "health.capabilities.v1";
  let outputDir: string | undefined;
  let node = process.execPath;
  let npm = "npm";

  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    switch (arg) {
      case "--track":
        track = parseTrack(requireValue(args, i, arg));
        i += 1;
        break;
      case "--from-channel":
        fromChannel = requireValue(args, i, arg);
        i += 1;
        break;
      case "--to-channel":
        toChannel = requireValue(args, i, arg);
        i += 1;
        break;
      case "--endpoint":
        endpoint = requireValue(args, i, arg);
        i += 1;
        break;
      case "--iterations":
        iterations = parsePositiveInt(requireValue(args, i, arg), arg);
        i += 1;
        break;
      case "--concurrency":
        concurrency = parsePositiveInt(requireValue(args, i, arg), arg);
        i += 1;
        break;
      case "--max-p95-latency-ms":
        maxP95LatencyMs = parsePositiveNumber(requireValue(args, i, arg), arg);
        i += 1;
        break;
      case "--min-tps":
        minTps = parsePositiveNumber(requireValue(args, i, arg), arg);
        i += 1;
        break;
      case "--max-tarball-bytes":
        maxTarballBytes = parsePositiveInt(requireValue(args, i, arg), arg);
        i += 1;
        break;
      case "--max-unpacked-bytes":
        maxUnpackedBytes = parsePositiveInt(requireValue(args, i, arg), arg);
        i += 1;
        break;
      case "--required-capability":
        requiredCapability = requireValue(args, i, arg);
        i += 1;
        break;
      case "--output-dir":
        outputDir = path.resolve(requireValue(args, i, arg));
        i += 1;
        break;
      case "--node":
        node = path.resolve(requireValue(args, i, arg));
        i += 1;
        break;
      case "--npm":
        npm = path.resolve(requireValue(args, i, arg));
        i += 1;
        break;
      default:
        throw new Error(`unknown argument: ${arg}`);
    }
  }

  if (track === "rc") {
    fromChannel = fromChannel === "edge" ? "nightly" : fromChannel;
    toChannel = toChannel === "nightly" ? "rc" : toChannel;
  }

  return {
    track,
    fromChannel,
    toChannel,
    endpoint,
    iterations: iterations ?? (track === "rc" ? 40 : 12),
    concurrency: concurrency ?? (track === "rc" ? 4 : 2),
    maxP95LatencyMs: maxP95LatencyMs ?? (track === "rc" ? 800 : 1500),
    minTps: minTps ?? (track === "rc" ? 4 : 1),
    maxTarballBytes,
    maxUnpackedBytes,
    requiredCapability,
    outputDir,
    node,
    npm,
  };
}

function parseTrack(value: string): GateTrack {
  if (value === "nightly" || value === "rc") {
    return value;
  }
  throw new Error(`--track must be nightly or rc, got ${value}`);
}

async function npmViewPackage(config: GateConfig, spec: string): Promise<NpmView> {
  const { stdout } = await runNpm(config, [
    "view",
    spec,
    "name",
    "version",
    "dist.integrity",
    "dist.unpackedSize",
    "dist.tarball",
    "--json",
  ]);
  const parsed: unknown = JSON.parse(stdout);
  const normalized = normalizeNpmView(parsed);
  if (normalized === undefined) {
    throw new Error(`npm view returned unexpected shape: ${stdout}`);
  }
  return normalized;
}

async function npmPack(config: GateConfig, spec: string, destination: string): Promise<NpmPackEntry> {
  const { stdout } = await runNpm(config, [
    "pack",
    spec,
    "--json",
    "--ignore-scripts",
    "--pack-destination",
    destination,
  ]);
  const parsed: unknown = JSON.parse(stdout);
  if (!Array.isArray(parsed) || !isNpmPackEntry(parsed[0])) {
    throw new Error(`npm pack returned unexpected shape: ${stdout}`);
  }
  return parsed[0];
}

async function npmInstall(config: GateConfig, tarballPath: string, installDir: string): Promise<void> {
  await runNpm(config, [
    "install",
    "--prefix",
    installDir,
    "--ignore-scripts",
    "--no-audit",
    "--no-fund",
    tarballPath,
  ]);
}

async function runNpm(
  config: GateConfig,
  args: readonly string[],
): Promise<{ readonly stdout: string; readonly stderr: string }> {
  return execFileAsync(config.node, [config.npm, ...args], {
    cwd: process.cwd(),
    encoding: "utf8",
    timeout: 180_000,
    env: {
      ...process.env,
      NPM_CONFIG_AUDIT: "false",
      NPM_CONFIG_FUND: "false",
      NPM_CONFIG_REGISTRY: "https://registry.npmjs.org/",
    },
  });
}

async function loadPublishedHealth(installDir: string): Promise<HealthFunction> {
  const modulePath = path.join(
    installDir,
    "node_modules",
    "@guardian-intelligence",
    "aisucks",
    "dist",
    "index.js",
  );
  const module: unknown = await import(pathToFileURL(modulePath).toString());
  if (!isHealthModule(module)) {
    throw new Error("published SDK does not export health()");
  }
  return module.health;
}

async function runSynthetic(
  config: GateConfig,
  npmView: NpmView,
  packEntry: NpmPackEntry,
  tarballBytes: number,
  health: HealthFunction,
): Promise<SyntheticResult> {
  const startedAt = new Date().toISOString();
  const latencies: number[] = [];
  const failures: string[] = [];
  const capabilities = new Set<string>();
  let next = 0;
  let successCount = 0;
  const wallStart = performance.now();

  async function worker(): Promise<void> {
    for (;;) {
      const current = next;
      next += 1;
      if (current >= config.iterations) {
        return;
      }
      const requestStart = performance.now();
      try {
        const response = await health({ baseUrl: config.endpoint });
        const elapsed = performance.now() - requestStart;
        latencies.push(elapsed);
        if (response.status !== "ok") {
          failures.push(`request ${current + 1}: status=${String(response.status)}`);
          continue;
        }
        for (const capability of response.capabilities ?? []) {
          capabilities.add(capability);
        }
        successCount += 1;
      } catch (error) {
        failures.push(
          `request ${current + 1}: ${error instanceof Error ? error.message : String(error)}`,
        );
      }
    }
  }

  await Promise.all(
    Array.from({ length: Math.min(config.concurrency, config.iterations) }, () => worker()),
  );
  const wallMs = performance.now() - wallStart;
  const endedAt = new Date().toISOString();
  const latency = latencySummary(latencies);
  const observedTps = round(successCount / Math.max(wallMs / 1000, 0.001));
  const unpackedBytes = packEntry.unpackedSize ?? npmView.dist.unpackedSize;
  const capabilityList = Array.from(capabilities).sort();
  if (!capabilities.has(config.requiredCapability)) {
    failures.push(`missing required capability ${config.requiredCapability}`);
  }

  return {
    schemaVersion: "guardian.synthetic-result.v1",
    package: npmView.name,
    version: npmView.version,
    sourceChannel: config.fromChannel,
    endpointUrl: config.endpoint,
    operation,
    status: failures.length === 0 && successCount === config.iterations ? "pass" : "fail",
    startedAt,
    endedAt,
    requestCount: config.iterations,
    successCount,
    failureCount: failures.length,
    latencyMs: latency,
    observedTps,
    packageBytes: {
      tarball: tarballBytes,
      unpacked: unpackedBytes,
    },
    capabilitiesObserved: capabilityList,
    failures,
  };
}

function gateResult(
  config: GateConfig,
  npmView: NpmView,
  synthetic: SyntheticResult,
  syntheticResultPath: string,
): GateResult {
  const checks: GateCheck[] = [
    {
      name: "synthetic_success",
      observed: `${synthetic.successCount}/${synthetic.requestCount}`,
      threshold: "all requests pass and required capability is observed",
      passed: synthetic.status === "pass",
    },
    {
      name: "health_p95_latency_ms",
      observed: synthetic.latencyMs.p95,
      threshold: `<= ${config.maxP95LatencyMs}`,
      passed: synthetic.latencyMs.p95 <= config.maxP95LatencyMs,
    },
    {
      name: "observed_tps",
      observed: synthetic.observedTps,
      threshold: `>= ${config.minTps}`,
      passed: synthetic.observedTps >= config.minTps,
    },
    {
      name: "npm_tarball_bytes",
      observed: synthetic.packageBytes.tarball,
      threshold: `<= ${config.maxTarballBytes}`,
      passed: synthetic.packageBytes.tarball <= config.maxTarballBytes,
    },
    {
      name: "npm_unpacked_bytes",
      observed: synthetic.packageBytes.unpacked,
      threshold: `<= ${config.maxUnpackedBytes}`,
      passed: synthetic.packageBytes.unpacked <= config.maxUnpackedBytes,
    },
  ];

  return {
    schemaVersion: "guardian.gate-result.v1",
    product: "aisucks",
    track: config.track,
    fromChannel: config.fromChannel,
    toChannel: config.toChannel,
    decision: checks.every((check) => check.passed) ? "pass" : "fail",
    package: npmView.name,
    version: npmView.version,
    endpointUrl: config.endpoint,
    operation,
    checkedAt: new Date().toISOString(),
    checks,
    metrics: {
      latencyMs: synthetic.latencyMs,
      observedTps: synthetic.observedTps,
      requestCount: synthetic.requestCount,
      successCount: synthetic.successCount,
      tarballBytes: synthetic.packageBytes.tarball,
      unpackedBytes: synthetic.packageBytes.unpacked,
    },
    syntheticResultPath,
  };
}

function renderSummary(gate: GateResult): string {
  const lines = [
    `# Aisucks ${gate.track} gate: ${gate.decision}`,
    "",
    `Package: \`${gate.package}@${gate.version}\``,
    `Endpoint: \`${gate.endpointUrl}\``,
    "",
    "| Check | Observed | Threshold | Result |",
    "| - | -: | - | - |",
  ];
  for (const check of gate.checks) {
    lines.push(
      `| ${check.name} | ${String(check.observed)} | ${check.threshold} | ${
        check.passed ? "pass" : "fail"
      } |`,
    );
  }
  lines.push("");
  lines.push(`p95 latency: ${gate.metrics.latencyMs.p95} ms`);
  lines.push(`max observed TPS: ${gate.metrics.observedTps}`);
  lines.push(`tarball bytes: ${gate.metrics.tarballBytes}`);
  lines.push(`unpacked bytes: ${gate.metrics.unpackedBytes}`);
  lines.push("");
  return `${lines.join("\n")}\n`;
}

function latencySummary(values: readonly number[]): SyntheticResult["latencyMs"] {
  if (values.length === 0) {
    return { min: 0, p50: 1_000_000_000, p95: 1_000_000_000, max: 0 };
  }
  const sorted = [...values].sort((a, b) => a - b);
  return {
    min: round(sorted[0] ?? 0),
    p50: round(percentile(sorted, 0.5)),
    p95: round(percentile(sorted, 0.95)),
    max: round(sorted[sorted.length - 1] ?? 0),
  };
}

function percentile(sorted: readonly number[], p: number): number {
  if (sorted.length === 0) {
    return Number.POSITIVE_INFINITY;
  }
  const index = Math.ceil(sorted.length * p) - 1;
  return sorted[Math.min(Math.max(index, 0), sorted.length - 1)] ?? Number.POSITIVE_INFINITY;
}

function round(value: number): number {
  return Math.round(value * 100) / 100;
}

async function writeJson(filePath: string, value: unknown): Promise<void> {
  await writeFile(filePath, `${JSON.stringify(value, null, 2)}\n`, "utf8");
  JSON.parse(await readFile(filePath, "utf8"));
}

function requireValue(args: readonly string[], index: number, flag: string): string {
  const value = args[index + 1];
  if (typeof value !== "string" || value === "" || value.startsWith("--")) {
    throw new Error(`${flag} requires a value`);
  }
  return value;
}

function parsePositiveInt(value: string, flag: string): number {
  const parsed = Number.parseInt(value, 10);
  if (!Number.isInteger(parsed) || parsed <= 0) {
    throw new Error(`${flag} must be a positive integer`);
  }
  return parsed;
}

function parsePositiveNumber(value: string, flag: string): number {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    throw new Error(`${flag} must be a positive number`);
  }
  return parsed;
}

function normalizeNpmView(value: unknown): NpmView | undefined {
  if (!isRecord(value)) {
    return undefined;
  }
  const dist = isRecord(value.dist)
    ? value.dist
    : {
        integrity: value["dist.integrity"],
        unpackedSize: value["dist.unpackedSize"],
        tarball: value["dist.tarball"],
      };
  if (
    value.name !== sdkPackageName ||
    typeof value.version !== "string" ||
    typeof dist.integrity !== "string" ||
    typeof dist.unpackedSize !== "number" ||
    typeof dist.tarball !== "string"
  ) {
    return undefined;
  }
  return {
    name: value.name,
    version: value.version,
    dist: {
      integrity: dist.integrity,
      unpackedSize: dist.unpackedSize,
      tarball: dist.tarball,
    },
  };
}

function isNpmPackEntry(value: unknown): value is NpmPackEntry {
  return (
    isRecord(value) &&
    typeof value.filename === "string" &&
    typeof value.integrity === "string" &&
    typeof value.size === "number" &&
    (value.unpackedSize === undefined || typeof value.unpackedSize === "number")
  );
}

function isHealthModule(value: unknown): value is { readonly health: HealthFunction } {
  return isRecord(value) && typeof value.health === "function";
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

import { readFileSync } from "node:fs";
import path from "node:path";
import { Cause, Effect, Exit, Layer, Option } from "effect";

import { renderReleaseError } from "./errors.js";
import { parseReleaseConfig } from "./parse.js";
import { makeMemoryLoggerLayer, NodeFileLayer, NodeProcessLayer } from "./providers.js";
import {
  decodeJsonSync,
  encodeJsonSync,
  PackageJsonSchema,
  ReleaseSummarySchema,
} from "./schemas.js";
import { runRelease } from "./state-machine.js";
import { packageRoot } from "./types.js";

const packageJson = decodeJsonSync(
  PackageJsonSchema,
  readFileSync(path.join(packageRoot, "package.json"), "utf8"),
);

if (typeof packageJson.version !== "string" || packageJson.version === "") {
  throw new Error("package.json is missing a release version");
}

let config: ReturnType<typeof parseReleaseConfig>;
try {
  config = parseReleaseConfig(process.argv.slice(2), packageJson.version);
} catch (error) {
  process.stderr.write(`${renderReleaseError(error)}\n`);
  process.exit(1);
}

const layer = Layer.mergeAll(NodeProcessLayer, NodeFileLayer, makeMemoryLoggerLayer());
const program = runRelease(config).pipe(Effect.provide(layer));

const exit = await Effect.runPromiseExit(program);
if (Exit.isSuccess(exit)) {
  const result = exit.value;
  process.stdout.write(
    `${encodeJsonSync(
      ReleaseSummarySchema,
      {
        status: "ok",
        mode: config.mode,
        package: result.target.packageName,
        version: result.target.version,
        channel: result.target.channel,
        outputDir: result.outputDir,
        ociDigest: result.candidate.oci.oci_digest,
        publishedOciDigest: result.publishedOci?.oci_digest,
        ociSignatureStatus: result.ociSignatureStatus,
        npmStatus: result.npmStatus,
      },
      { pretty: true },
    )}\n`,
  );
} else {
  const failure = Cause.failureOption(exit.cause);
  const rendered = Option.isSome(failure)
    ? renderReleaseError(failure.value)
    : Cause.pretty(exit.cause);
  process.stderr.write(`${rendered}\n`);
  process.exitCode = 1;
}

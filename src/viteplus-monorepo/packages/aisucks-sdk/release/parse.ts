import path from "node:path";

import { ReleaseUsageError } from "./errors.js";
import {
  defaultChannel,
  defaultOciRef,
  defaultReleasePaths,
  releasePathsForRepoRoot,
  type ReleaseConfig,
  type ReleaseMode,
} from "./types.js";

export function parseReleaseConfig(args: readonly string[], packageVersion: string): ReleaseConfig {
  let mode: ReleaseMode = "check";
  let version = packageVersion;
  let channel = defaultChannel;
  let ociRef = defaultOciRef;
  let publishNpm: boolean | undefined;
  let publishOci: boolean | undefined;
  let allowUnsignedDev = true;
  let outputDir: string | undefined;
  let sourceRoot: string | undefined;
  let bazelisk: string | undefined;
  let sdkoci: string | undefined;
  let cosign: string | undefined;
  let oras: string | undefined;
  let npm: string | undefined;
  let node: string | undefined;

  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    switch (arg) {
      case "--mode": {
        const value = requireValue(args, i, arg);
        if (value !== "check" && value !== "publish") {
          throw new ReleaseUsageError({
            reason: "--mode must be check or publish",
            details: { value },
          });
        }
        mode = value;
        i += 1;
        break;
      }
      case "--publish":
        mode = "publish";
        break;
      case "--version":
        version = requireValue(args, i, arg);
        i += 1;
        break;
      case "--channel":
        channel = requireValue(args, i, arg);
        i += 1;
        break;
      case "--oci-ref":
        ociRef = requireValue(args, i, arg);
        i += 1;
        break;
      case "--skip-npm":
        publishNpm = false;
        break;
      case "--skip-oci":
        publishOci = false;
        break;
      case "--require-signature":
        allowUnsignedDev = false;
        break;
      case "--output-dir":
        outputDir = path.resolve(requireValue(args, i, arg));
        i += 1;
        break;
      case "--source-root":
        sourceRoot = path.resolve(requireValue(args, i, arg));
        i += 1;
        break;
      case "--bazelisk":
        bazelisk = requireValue(args, i, arg);
        i += 1;
        break;
      case "--sdkoci":
        sdkoci = path.resolve(requireValue(args, i, arg));
        i += 1;
        break;
      case "--cosign":
        cosign = requireValue(args, i, arg);
        i += 1;
        break;
      case "--oras":
        oras = requireValue(args, i, arg);
        i += 1;
        break;
      case "--npm":
        npm = path.resolve(requireValue(args, i, arg));
        i += 1;
        break;
      case "--node":
        node = path.resolve(requireValue(args, i, arg));
        i += 1;
        break;
      default:
        throw new ReleaseUsageError({
          reason: `unknown argument: ${arg}`,
          details: { args },
        });
    }
  }

  const basePaths =
    sourceRoot === undefined ? defaultReleasePaths() : releasePathsForRepoRoot(sourceRoot);

  return {
    mode,
    version,
    channel,
    ociRef,
    publishNpm: publishNpm ?? mode === "publish",
    publishOci: publishOci ?? mode === "publish",
    allowUnsignedDev,
    outputDir,
    paths: {
      ...basePaths,
      bazelisk: bazelisk ?? basePaths.bazelisk,
      sdkoci: sdkoci ?? basePaths.sdkoci,
      cosign: cosign ?? basePaths.cosign,
      oras: oras ?? basePaths.oras,
      npm: npm ?? basePaths.npm,
      node: node ?? basePaths.node,
    },
  };
}

function requireValue(args: readonly string[], index: number, flag: string): string {
  const value = args[index + 1];
  if (typeof value !== "string" || value === "" || value.startsWith("--")) {
    throw new ReleaseUsageError({ reason: `${flag} requires a value` });
  }
  return value;
}

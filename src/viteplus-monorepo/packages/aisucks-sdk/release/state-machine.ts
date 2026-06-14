import path from "node:path";
import { Effect } from "effect";

import { admitRelease } from "./admit.js";
import { readPackEntry, verifyTarballBytes } from "./digest.js";
import {
  InvalidReleaseTarget,
  PublishConflict,
  VerificationFailed,
  type ReleaseError,
} from "./errors.js";
import { createEvidenceBundle } from "./intoto.js";
import { FileProvider, LoggerProvider, ProcessProvider, type CommandInput } from "./providers.js";
import { retryTransient } from "./retry.js";
import {
  decodeJson,
  encodeJson,
  fileJson,
  invalidJson,
  NpmIntegrityViewSchema,
  PackageJsonSchema,
  ReleaseResultSchema,
  SdkOciResultSchema,
  verificationJson,
} from "./schemas.js";
import {
  sdkPackageName,
  sourceRepo,
  type EvidenceBundle,
  type NpmPackEntry,
  type ReleaseCandidate,
  type ReleaseConfig,
  type ReleaseResult,
  type ReleaseTarget,
  type SdkOciResult,
} from "./types.js";

const commandTimeoutMs = 120_000;
const buildTimeoutMs = 600_000;
const publishTimeoutMs = 180_000;

export function runRelease(
  config: ReleaseConfig,
): Effect.Effect<ReleaseResult, ReleaseError, FileProvider | LoggerProvider | ProcessProvider> {
  return Effect.gen(function* () {
    const outputDir =
      config.outputDir ??
      (yield* stage(
        "workspace",
        "create release workspace",
        Effect.gen(function* () {
          const files = yield* FileProvider;
          return yield* files.mkdtemp("guardian-aisucks-sdk-release-");
        }),
      ));

    yield* stage("preflight", "validate release target", preflight(config));
    const target = yield* stage("target", "resolve source commit", resolveTarget(config));
    yield* stage(
      "build",
      "build package-owned release inputs through Bazel",
      buildInputs(config, target),
    );
    const candidate = yield* stage(
      "candidate",
      "create local OCI candidate",
      createLocalCandidate(config, target, outputDir),
    );
    const evidence = yield* stage(
      "attest",
      "create DSSE in-toto/SLSA evidence",
      createEvidenceBundle(config, candidate, outputDir),
    );
    yield* stage(
      "admit",
      "validate candidate and evidence before public writes",
      admitRelease(config, candidate, evidence),
    );
    const admittedCandidate = yield* stage(
      "local-evidence",
      "attach admitted evidence to local OCI layout",
      attachLocalEvidence(config, candidate, evidence, outputDir),
    );
    const publishedOci = yield* stage(
      "publish-oci",
      "publish admitted OCI subject",
      publishOci(config, admittedCandidate, evidence, outputDir),
    );
    const npmStatus = yield* stage(
      "publish-npm",
      "publish npm package projection",
      publishNpm(config, admittedCandidate),
    );
    yield* stage(
      "verify",
      "verify final release projections",
      verifyRelease(config, admittedCandidate, publishedOci, npmStatus),
    );

    const files = yield* FileProvider;
    const logger = yield* LoggerProvider;
    const result: ReleaseResult = {
      target,
      candidate: admittedCandidate,
      evidence,
      publishedOci,
      npmStatus,
      eventLog: logger.events(),
      outputDir,
    };
    yield* files.writeFile(
      path.join(outputDir, "release-result.json"),
      `${yield* encodeJson(
        ReleaseResultSchema,
        result,
        (reason) => fileJson("encodeJson", path.join(outputDir, "release-result.json"))(reason),
        { pretty: true },
      )}\n`,
    );
    return result;
  });
}

function preflight(config: ReleaseConfig): Effect.Effect<void, ReleaseError, FileProvider> {
  return Effect.gen(function* () {
    if (!/^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(config.version)) {
      return yield* Effect.fail(
        new InvalidReleaseTarget({
          reason: "release version is not a semver-like npm version",
          details: { version: config.version },
        }),
      );
    }
    if (config.channel === "") {
      return yield* Effect.fail(new InvalidReleaseTarget({ reason: "release channel is empty" }));
    }
    if (config.mode === "publish" && !config.publishNpm && !config.publishOci) {
      return yield* Effect.fail(
        new InvalidReleaseTarget({
          reason: "publish mode has no enabled public projection",
        }),
      );
    }
    if (config.mode === "publish" && config.publishNpm && process.env.GITHUB_ACTIONS === "true") {
      if (
        process.env.ACTIONS_ID_TOKEN_REQUEST_URL === undefined ||
        process.env.ACTIONS_ID_TOKEN_REQUEST_TOKEN === undefined
      ) {
        return yield* Effect.fail(
          new InvalidReleaseTarget({
            reason: "npm trusted publishing requires GitHub Actions OIDC",
            details: {
              requiredPermission: "id-token: write",
              workflow: ".github/workflows/npm-sdk-release.yml",
            },
          }),
        );
      }
    }

    const files = yield* FileProvider;
    const packageJsonPath = path.join(config.paths.packageRoot, "package.json");
    const packageJson = yield* decodeJson(
      PackageJsonSchema,
      (yield* files.readFile(packageJsonPath)).toString("utf8"),
      (reason) => invalidJson("package.json does not match release schema", { reason }),
    );
    if (packageJson.name !== sdkPackageName || packageJson.version !== config.version) {
      return yield* Effect.fail(
        new InvalidReleaseTarget({
          reason: "package.json does not match release target",
          details: {
            expectedName: sdkPackageName,
            actualName: packageJson.name,
            expectedVersion: config.version,
            actualVersion: packageJson.version,
          },
        }),
      );
    }
  });
}

function resolveTarget(
  config: ReleaseConfig,
): Effect.Effect<ReleaseTarget, ReleaseError, ProcessProvider> {
  return Effect.gen(function* () {
    const sourceCommit = (yield* runCommand({
      program: "git",
      args: ["rev-parse", "HEAD"],
      cwd: config.paths.repoRoot,
      timeoutMs: commandTimeoutMs,
    })).stdout.trim();
    if (!/^[0-9a-f]{40}$/.test(sourceCommit)) {
      return yield* Effect.fail(
        new InvalidReleaseTarget({
          reason: "git HEAD did not resolve to a full commit SHA",
          details: { sourceCommit },
        }),
      );
    }

    return {
      packageName: sdkPackageName,
      version: config.version,
      channel: config.channel,
      sourceRepo,
      sourceCommit,
      ociRef: config.ociRef,
    };
  });
}

function buildInputs(
  config: ReleaseConfig,
  target: ReleaseTarget,
): Effect.Effect<void, ReleaseError, ProcessProvider> {
  return runCommand({
    program: config.paths.bazelisk,
    args: [
      "build",
      `--embed_label=${target.sourceCommit}`,
      "//src/viteplus-monorepo:vp_node",
      "//src/viteplus-monorepo/packages/aisucks-sdk:npm_package",
      "//src/release/cmd/sdkoci",
    ],
    cwd: config.paths.repoRoot,
    timeoutMs: buildTimeoutMs,
  }).pipe(Effect.asVoid);
}

function createLocalCandidate(
  config: ReleaseConfig,
  target: ReleaseTarget,
  outputDir: string,
): Effect.Effect<ReleaseCandidate, ReleaseError, FileProvider | ProcessProvider> {
  return Effect.gen(function* () {
    const localLayout = path.join(outputDir, "oci-layout");
    const resultPath = path.join(outputDir, "sdk-oci.local.json");
    yield* runSdkOci(config, [
      "--tarball",
      config.paths.tarball,
      "--pack-json",
      config.paths.packJson,
      "--oci-layout",
      localLayout,
      "--tag",
      target.channel,
      "--source-commit",
      target.sourceCommit,
      "--output",
      resultPath,
    ]);

    const pack = yield* readPackEntry(config.paths.packJson);
    const tarball = yield* verifyTarballBytes(config.paths.tarball, pack);
    const oci = yield* readSdkOciResult(resultPath);

    return {
      target,
      pack,
      oci,
      tarballSha256: tarball.sha256,
      npmIntegrity: tarball.integrity,
      localLayout,
    };
  });
}

function attachLocalEvidence(
  config: ReleaseConfig,
  candidate: ReleaseCandidate,
  evidence: EvidenceBundle,
  outputDir: string,
): Effect.Effect<ReleaseCandidate, ReleaseError, FileProvider | ProcessProvider> {
  return Effect.gen(function* () {
    const resultPath = path.join(outputDir, "sdk-oci.local.admitted.json");
    yield* runSdkOci(config, [
      "--tarball",
      config.paths.tarball,
      "--pack-json",
      config.paths.packJson,
      "--oci-layout",
      candidate.localLayout,
      "--tag",
      candidate.target.channel,
      "--source-commit",
      candidate.target.sourceCommit,
      "--attestation-bundle",
      evidence.intotoBundlePath,
      "--attestation-title",
      `${candidate.pack.filename}.intoto.jsonl`,
      "--output",
      resultPath,
    ]);

    const oci = yield* readSdkOciResult(resultPath);
    if (oci.oci_digest !== candidate.oci.oci_digest) {
      return yield* Effect.fail(
        new VerificationFailed({
          reason: "local evidence attach changed the admitted OCI digest",
          details: {
            admitted: candidate.oci.oci_digest,
            sealed: oci.oci_digest,
          },
        }),
      );
    }
    if (oci.attestation_digest === undefined) {
      return yield* Effect.fail(
        new VerificationFailed({
          reason: "local evidence attach did not produce an attestation referrer digest",
        }),
      );
    }
    return {
      ...candidate,
      oci,
    };
  });
}

function publishOci(
  config: ReleaseConfig,
  candidate: ReleaseCandidate,
  evidence: EvidenceBundle,
  outputDir: string,
): Effect.Effect<SdkOciResult | undefined, ReleaseError, FileProvider | ProcessProvider> {
  if (!config.publishOci) {
    return Effect.succeed(undefined);
  }

  return Effect.gen(function* () {
    const resultPath = path.join(outputDir, "sdk-oci.public.json");
    const authArgs =
      process.env.GUARDIAN_OCI_ACCESS_TOKEN !== undefined
        ? ["--access-token-env", "GUARDIAN_OCI_ACCESS_TOKEN"]
        : [
            "--username",
            process.env.GUARDIAN_OCI_USERNAME ?? "guardian-release",
            "--password-env",
            "GUARDIAN_OCI_PASSWORD",
          ];

    yield* runSdkOci(config, [
      "--tarball",
      config.paths.tarball,
      "--pack-json",
      config.paths.packJson,
      "--ref",
      config.ociRef,
      "--source-commit",
      candidate.target.sourceCommit,
      "--attestation-bundle",
      evidence.intotoBundlePath,
      "--attestation-title",
      `${candidate.pack.filename}.intoto.jsonl`,
      "--output",
      resultPath,
      ...authArgs,
    ]);

    return yield* readSdkOciResult(resultPath);
  });
}

function publishNpm(
  config: ReleaseConfig,
  candidate: ReleaseCandidate,
): Effect.Effect<
  "not-requested" | "published" | "already-published",
  ReleaseError,
  ProcessProvider
> {
  if (!config.publishNpm) {
    return Effect.succeed("not-requested");
  }

  return Effect.gen(function* () {
    const existing = yield* npmViewIntegrity(config, candidate.pack).pipe(
      Effect.catchAll((error) => {
        if (error._tag === "CommandFailed" && isNpmNotFound(error.stderr)) {
          return Effect.succeed(undefined);
        }
        return Effect.fail(error);
      }),
    );
    if (existing !== undefined) {
      if (existing === candidate.npmIntegrity) {
        return "already-published";
      }
      return yield* Effect.fail(
        new PublishConflict({
          reason: "npm version already exists with different integrity",
          details: {
            package: candidate.pack.name,
            version: candidate.pack.version,
            expected: candidate.npmIntegrity,
            actual: existing,
          },
        }),
      );
    }

    yield* retryTransient(
      runNpm(
        config,
        [
          "publish",
          config.paths.tarball,
          "--tag",
          candidate.target.channel,
          "--access",
          "public",
          "--provenance",
        ],
        publishTimeoutMs,
      ),
      isTransientReleaseError,
    );
    return "published";
  });
}

function verifyRelease(
  config: ReleaseConfig,
  candidate: ReleaseCandidate,
  publishedOci: SdkOciResult | undefined,
  npmStatus: "not-requested" | "published" | "already-published",
): Effect.Effect<void, ReleaseError, ProcessProvider> {
  return Effect.gen(function* () {
    if (publishedOci !== undefined) {
      if (publishedOci.oci_digest !== candidate.oci.oci_digest) {
        return yield* Effect.fail(
          new VerificationFailed({
            reason: "published OCI digest does not match admitted local candidate",
            details: {
              local: candidate.oci.oci_digest,
              published: publishedOci.oci_digest,
            },
          }),
        );
      }
      if (publishedOci.attestation_digest === undefined) {
        return yield* Effect.fail(
          new VerificationFailed({
            reason: "published OCI result did not include attestation referrer digest",
          }),
        );
      }
    }

    if (npmStatus !== "not-requested") {
      const integrity = yield* npmViewIntegrity(config, candidate.pack);
      if (integrity !== candidate.npmIntegrity) {
        return yield* Effect.fail(
          new VerificationFailed({
            reason: "npm registry integrity does not match admitted tarball",
            details: {
              package: candidate.pack.name,
              version: candidate.pack.version,
              expected: candidate.npmIntegrity,
              actual: integrity,
            },
          }),
        );
      }
    }
  });
}

function npmViewIntegrity(
  config: ReleaseConfig,
  pack: NpmPackEntry,
): Effect.Effect<string, ReleaseError, ProcessProvider> {
  return retryTransient(
    runNpm(
      config,
      ["view", `${pack.name}@${pack.version}`, "dist.integrity", "--json"],
      commandTimeoutMs,
    ).pipe(
      Effect.flatMap((result) =>
        Effect.gen(function* () {
          const parsed = yield* decodeJson(NpmIntegrityViewSchema, result.stdout, (reason) =>
            verificationJson("npm view dist.integrity did not match schema", { reason }),
          );
          if (parsed === "") {
            return yield* Effect.fail(
              new VerificationFailed({
                reason: "npm view did not return dist.integrity",
                details: { stdout: result.stdout },
              }),
            );
          }
          return parsed;
        }),
      ),
    ),
    isTransientReleaseError,
  );
}

function runNpm(
  config: ReleaseConfig,
  args: readonly string[],
  timeoutMs: number,
): Effect.Effect<import("./types.js").CommandResult, ReleaseError, ProcessProvider> {
  return runCommand({
    program: config.paths.node,
    args: [config.paths.npm, ...args],
    cwd: config.paths.packageRoot,
    timeoutMs,
  });
}

function runSdkOci(
  config: ReleaseConfig,
  args: readonly string[],
): Effect.Effect<void, ReleaseError, ProcessProvider> {
  return retryTransient(
    runCommand({
      program: config.paths.sdkoci,
      args,
      cwd: config.paths.repoRoot,
      timeoutMs: publishTimeoutMs,
    }),
    isTransientReleaseError,
  ).pipe(Effect.asVoid);
}

function readSdkOciResult(
  filePath: string,
): Effect.Effect<SdkOciResult, ReleaseError, FileProvider> {
  return Effect.gen(function* () {
    const files = yield* FileProvider;
    const raw = yield* files.readFile(filePath);
    return yield* decodeJson(SdkOciResultSchema, raw.toString("utf8"), (reason) =>
      fileJson("decodeJson", filePath)(reason),
    );
  });
}

function runCommand(
  input: CommandInput,
): Effect.Effect<import("./types.js").CommandResult, ReleaseError, ProcessProvider> {
  return Effect.gen(function* () {
    const processProvider = yield* ProcessProvider;
    return yield* processProvider.run(input);
  });
}

function stage<A, R>(
  stageName: string,
  message: string,
  effect: Effect.Effect<A, ReleaseError, R>,
): Effect.Effect<A, ReleaseError, R | LoggerProvider> {
  return Effect.gen(function* () {
    const logger = yield* LoggerProvider;
    const started = Date.now();
    yield* logger.log({ stage: stageName, status: "start", message });
    const result = yield* effect.pipe(
      Effect.tap(() =>
        logger.log({
          stage: stageName,
          status: "ok",
          message,
          elapsedMs: Date.now() - started,
        }),
      ),
      Effect.tapError((error) =>
        logger.log({
          stage: stageName,
          status: "fail",
          message,
          elapsedMs: Date.now() - started,
          details: { tag: error._tag },
        }),
      ),
    );
    return result;
  });
}

function isNpmNotFound(stderr: string): boolean {
  return stderr.includes("E404") || stderr.includes("404 Not Found");
}

function isTransientReleaseError(error: ReleaseError): boolean {
  if (error._tag !== "CommandFailed" && error._tag !== "CommandTimedOut") {
    return false;
  }
  const text = `${"stderr" in error ? error.stderr : ""}\n${"stdout" in error ? error.stdout : ""}`;
  return (
    error._tag === "CommandTimedOut" ||
    text.includes("ECONNRESET") ||
    text.includes("ETIMEDOUT") ||
    text.includes("EAI_AGAIN") ||
    text.includes("502 Bad Gateway") ||
    text.includes("503 Service Unavailable") ||
    text.includes("504 Gateway Timeout")
  );
}

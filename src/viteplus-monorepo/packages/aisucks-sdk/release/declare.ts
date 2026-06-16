import path from "node:path";
import { fileURLToPath } from "node:url";
import { Effect, Exit, Layer, Cause, Option } from "effect";

import {
  AdmissionRejected,
  ReleaseUsageError,
  VerificationFailed,
  type ReleaseError,
} from "./errors.js";
import { renderReleaseError } from "./errors.js";
import {
  FileProvider,
  LoggerProvider,
  NodeFileLayer,
  NodeProcessLayer,
  ProcessProvider,
  makeMemoryLoggerLayer,
  type CommandInput,
} from "./providers.js";
import { decodeUnknown, InTotoStatementSchema } from "./schemas.js";
import {
  cosignReleaseEnv,
  cosignVerifyArgs,
  cosignVerifyAttestationArgs,
} from "./state-machine.js";
import {
  defaultReleasePaths,
  githubOidcIssuer,
  inTotoStatementType,
  releasePathsForRepoRoot,
  sdkPackageName,
  sdkReleaseWorkflowIdentity,
  slsaProvenancePredicateType,
  sourceRepo,
  type InTotoStatement,
  type ReleaseEvent,
  type ReleasePaths,
} from "./types.js";

const commandTimeoutMs = 120_000;
const sdkProduct = "aisucks";
const sdkOciRepository = "oci.guardianintelligence.org/guardian/aisucks/sdk/npm";
const sdkReleaseBuilderId =
  "https://github.com/guardian-intelligence/guardian/.github/workflows/npm-sdk-release.yml";
const trackValues = ["edge", "nightly", "rc", "stable"] as const;

type ReleaseTrack = (typeof trackValues)[number];

type ReleaseDeclarationConfig = {
  readonly product: string;
  readonly version: string;
  readonly commit: string;
  readonly track: ReleaseTrack;
  readonly ociRef: string;
  readonly outputDir: string | undefined;
  readonly paths: Pick<ReleasePaths, "repoRoot" | "cosign" | "oras">;
};

type ReleaseDeclaration = {
  readonly product: string;
  readonly package: string;
  readonly version: string;
  readonly track: ReleaseTrack;
  readonly sourceRepo: string;
  readonly sourceCommit: string;
  readonly requestedRef: string;
  readonly subjectRef: string;
  readonly subjectDigest: string;
  readonly verification: {
    readonly signature: "verified";
    readonly attestation: "verified";
    readonly certificateIdentity: string;
    readonly oidcIssuer: string;
    readonly predicateType: string;
  };
  readonly outputDir: string;
  readonly eventLog: readonly ReleaseEvent[];
};

if (isEntrypoint()) {
  await runDeclarationCli(process.argv.slice(2));
}

export async function runDeclarationCli(args: readonly string[]): Promise<void> {
  let config: ReleaseDeclarationConfig;
  try {
    config = parseDeclarationConfig(args);
  } catch (error) {
    process.stderr.write(`${renderReleaseError(error)}\n`);
    process.exit(1);
  }

  const layer = Layer.mergeAll(NodeProcessLayer, NodeFileLayer, makeMemoryLoggerLayer());
  const program = declareRelease(config).pipe(Effect.provide(layer));
  const exit = await Effect.runPromiseExit(program);
  if (Exit.isSuccess(exit)) {
    process.stdout.write(`${JSON.stringify(exit.value, null, 2)}\n`);
  } else {
    const failure = Cause.failureOption(exit.cause);
    const rendered = Option.isSome(failure)
      ? renderReleaseError(failure.value)
      : Cause.pretty(exit.cause);
    process.stderr.write(`${rendered}\n`);
    process.exitCode = 1;
  }
}

export function parseDeclarationConfig(args: readonly string[]): ReleaseDeclarationConfig {
  let product: string | undefined;
  let version: string | undefined;
  let commit: string | undefined;
  let track: ReleaseTrack | undefined;
  let ociRef: string | undefined;
  let outputDir: string | undefined;
  let sourceRoot: string | undefined;
  let cosign: string | undefined;
  let oras: string | undefined;

  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    switch (arg) {
      case "--product":
        product = requireValue(args, i, arg);
        i += 1;
        break;
      case "--version":
        version = requireValue(args, i, arg);
        i += 1;
        break;
      case "--commit":
        commit = requireValue(args, i, arg);
        i += 1;
        break;
      case "--track": {
        const value = requireValue(args, i, arg);
        if (!isReleaseTrack(value)) {
          throw new ReleaseUsageError({
            reason: "--track must be edge, nightly, rc, or stable",
            details: { value },
          });
        }
        track = value;
        i += 1;
        break;
      }
      case "--oci-ref":
        ociRef = requireValue(args, i, arg);
        i += 1;
        break;
      case "--output-dir":
        outputDir = path.resolve(requireValue(args, i, arg));
        i += 1;
        break;
      case "--source-root":
        sourceRoot = path.resolve(requireValue(args, i, arg));
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
      default:
        throw new ReleaseUsageError({
          reason: `unknown argument: ${arg}`,
          details: { args },
        });
    }
  }

  if (product === undefined) {
    throw new ReleaseUsageError({ reason: "--product is required" });
  }
  if (version === undefined) {
    throw new ReleaseUsageError({ reason: "--version is required" });
  }
  if (commit === undefined) {
    throw new ReleaseUsageError({ reason: "--commit is required" });
  }
  if (track === undefined) {
    throw new ReleaseUsageError({ reason: "--track is required" });
  }

  const basePaths =
    sourceRoot === undefined ? defaultReleasePaths() : releasePathsForRepoRoot(sourceRoot);
  return {
    product,
    version,
    commit,
    track,
    ociRef: ociRef ?? defaultSdkVersionRef(version),
    outputDir,
    paths: {
      repoRoot: basePaths.repoRoot,
      cosign: cosign ?? basePaths.cosign,
      oras: oras ?? basePaths.oras,
    },
  };
}

export function declareRelease(
  config: ReleaseDeclarationConfig,
): Effect.Effect<
  ReleaseDeclaration,
  ReleaseError,
  FileProvider | LoggerProvider | ProcessProvider
> {
  return Effect.gen(function* () {
    const outputDir =
      config.outputDir ??
      (yield* stage(
        "workspace",
        "create declaration workspace",
        Effect.gen(function* () {
          const files = yield* FileProvider;
          return yield* files.mkdtemp("guardian-release-declare-");
        }),
      ));

    yield* stage("preflight", "validate release declaration", validateDeclarationConfig(config));
    const subjectRef = yield* stage(
      "resolve",
      "resolve declaration OCI subject",
      resolveOciSubject(config),
    );
    const statement = yield* stage(
      "verify",
      "verify declaration signature and SLSA provenance",
      verifyDeclarationContract(config, subjectRef),
    );
    yield* stage(
      "admit",
      "admit declared subject",
      admitDeclaredSubject(config, subjectRef, statement),
    );

    const logger = yield* LoggerProvider;
    const declaration: ReleaseDeclaration = {
      product: config.product,
      package: sdkPackageName,
      version: config.version,
      track: config.track,
      sourceRepo,
      sourceCommit: config.commit,
      requestedRef: config.ociRef,
      subjectRef,
      subjectDigest: digestFromRef(subjectRef),
      verification: {
        signature: "verified",
        attestation: "verified",
        certificateIdentity: sdkReleaseWorkflowIdentity,
        oidcIssuer: githubOidcIssuer,
        predicateType: slsaProvenancePredicateType,
      },
      outputDir,
      eventLog: logger.events(),
    };
    const files = yield* FileProvider;
    yield* files.writeFile(
      path.join(outputDir, "release-declaration.json"),
      `${JSON.stringify(declaration, null, 2)}\n`,
    );
    return declaration;
  });
}

function validateDeclarationConfig(
  config: ReleaseDeclarationConfig,
): Effect.Effect<void, ReleaseError> {
  return Effect.gen(function* () {
    yield* expect(config.product === sdkProduct, "unsupported release product", {
      actual: config.product,
      expected: sdkProduct,
    });
    yield* expect(
      /^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(config.version),
      "release version is not a semver-like npm version",
      { version: config.version },
    );
    yield* expect(/^[0-9a-f]{40}$/.test(config.commit), "release commit is not a full SHA", {
      commit: config.commit,
    });
    yield* expect(
      config.ociRef.startsWith(`${sdkOciRepository}:`) ||
        config.ociRef.startsWith(`${sdkOciRepository}@`),
      "declaration OCI ref is outside the aisucks SDK repository",
      {
        ociRef: config.ociRef,
        expectedRepository: sdkOciRepository,
      },
    );
  });
}

function resolveOciSubject(
  config: ReleaseDeclarationConfig,
): Effect.Effect<string, ReleaseError, ProcessProvider> {
  return Effect.gen(function* () {
    const result = yield* runCommand({
      program: config.paths.oras,
      args: ["resolve", "--full-reference", config.ociRef],
      cwd: config.paths.repoRoot,
      timeoutMs: commandTimeoutMs,
    }).pipe(
      Effect.mapError(
        (error) =>
          new VerificationFailed({
            reason: "declared OCI subject could not be resolved",
            details: releaseErrorDetails(error),
          }),
      ),
    );
    const resolved = yield* normalizeResolvedRef(config.ociRef, result.stdout);
    yield* expect(
      resolved.startsWith(`${sdkOciRepository}@`),
      "resolved OCI subject is outside the aisucks SDK repository",
      {
        requestedRef: config.ociRef,
        resolvedRef: resolved,
      },
    );
    return resolved;
  });
}

function verifyDeclarationContract(
  config: ReleaseDeclarationConfig,
  subjectRef: string,
): Effect.Effect<InTotoStatement, ReleaseError, ProcessProvider> {
  return Effect.gen(function* () {
    yield* runCommand({
      program: config.paths.cosign,
      args: cosignVerifyArgs(subjectRef),
      cwd: config.paths.repoRoot,
      env: cosignReleaseEnv(),
      timeoutMs: commandTimeoutMs,
    }).pipe(
      Effect.mapError(
        (error) =>
          new VerificationFailed({
            reason: "declared OCI signature verification failed",
            details: releaseErrorDetails(error),
          }),
      ),
    );

    const attestation = yield* runCommand({
      program: config.paths.cosign,
      args: cosignVerifyAttestationArgs(subjectRef),
      cwd: config.paths.repoRoot,
      env: cosignReleaseEnv(),
      timeoutMs: commandTimeoutMs,
    }).pipe(
      Effect.mapError(
        (error) =>
          new VerificationFailed({
            reason: "declared OCI SLSA attestation verification failed",
            details: releaseErrorDetails(error),
          }),
      ),
    );
    return yield* selectVerifiedSlsaStatement(attestation.stdout);
  });
}

function selectVerifiedSlsaStatement(stdout: string): Effect.Effect<InTotoStatement, ReleaseError> {
  return Effect.gen(function* () {
    const candidates = yield* parseCosignAttestationOutput(stdout);
    for (const candidate of candidates) {
      const decoded = yield* decodeUnknown(
        InTotoStatementSchema,
        candidate,
        (reason) =>
          new VerificationFailed({
            reason: "cosign verify-attestation returned an invalid in-toto statement",
            details: { reason },
          }),
      );
      if (decoded.predicateType === slsaProvenancePredicateType) {
        return decoded;
      }
    }
    return yield* Effect.fail(
      new VerificationFailed({
        reason: "cosign verify-attestation returned no SLSA provenance statement",
      }),
    );
  });
}

function parseCosignAttestationOutput(
  stdout: string,
): Effect.Effect<readonly unknown[], ReleaseError> {
  return Effect.gen(function* () {
    const values = yield* parseJsonOutput(stdout);
    const statements: unknown[] = [];
    for (const value of values) {
      for (const item of Array.isArray(value) ? value : [value]) {
        const statement = yield* statementFromCosignValue(item);
        if (statement !== undefined) {
          statements.push(statement);
        }
      }
    }
    if (statements.length === 0) {
      return yield* Effect.fail(
        new VerificationFailed({
          reason: "cosign verify-attestation output did not contain a DSSE payload",
        }),
      );
    }
    return statements;
  });
}

function statementFromCosignValue(value: unknown): Effect.Effect<unknown, ReleaseError> {
  return Effect.gen(function* () {
    if (!isRecord(value)) {
      return undefined;
    }
    if (
      value["_type"] !== undefined &&
      value["subject"] !== undefined &&
      value["predicateType"] !== undefined
    ) {
      return value;
    }
    const payload = value["payload"];
    if (typeof payload !== "string") {
      return undefined;
    }
    return yield* Effect.try({
      try: () => JSON.parse(Buffer.from(payload, "base64").toString("utf8")) as unknown,
      catch: (error) =>
        new VerificationFailed({
          reason: "failed to decode DSSE payload from cosign verify-attestation",
          details: { error: error instanceof Error ? error.message : String(error) },
        }),
    });
  });
}

function admitDeclaredSubject(
  config: ReleaseDeclarationConfig,
  subjectRef: string,
  statement: InTotoStatement,
): Effect.Effect<void, ReleaseError> {
  return Effect.gen(function* () {
    yield* expect(statement._type === inTotoStatementType, "SLSA statement type mismatch", {
      actual: statement._type,
      expected: inTotoStatementType,
    });
    yield* expect(
      statement.predicateType === slsaProvenancePredicateType,
      "SLSA predicate type mismatch",
      {
        actual: statement.predicateType,
        expected: slsaProvenancePredicateType,
      },
    );

    const subject = statement.subject.find((item) => item.name === subjectRef);
    yield* expect(subject !== undefined, "SLSA provenance missing declared OCI subject", {
      subjectRef,
    });
    yield* expect(
      subject?.digest.sha256 === digestFromRef(subjectRef).replace(/^sha256:/, ""),
      "SLSA provenance subject digest mismatch",
      {
        actual: subject?.digest.sha256,
        expected: digestFromRef(subjectRef).replace(/^sha256:/, ""),
      },
    );

    const releaseTarget = releaseTargetFromStatement(statement);
    yield* expect(
      releaseTarget?.package === sdkPackageName,
      "SLSA release target package mismatch",
      {
        actual: releaseTarget?.package,
        expected: sdkPackageName,
      },
    );
    yield* expect(
      releaseTarget?.version === config.version,
      "SLSA release target version mismatch",
      {
        actual: releaseTarget?.version,
        expected: config.version,
      },
    );

    const commits = resolvedDependencyCommits(statement);
    yield* expect(commits.includes(config.commit), "SLSA provenance source commit mismatch", {
      actual: commits,
      expected: config.commit,
      sourceRepo,
    });
    yield* expect(
      builderIdFromStatement(statement) === sdkReleaseBuilderId,
      "SLSA builder id mismatch",
      {
        actual: builderIdFromStatement(statement),
        expected: sdkReleaseBuilderId,
      },
    );
  });
}

function releaseTargetFromStatement(
  statement: InTotoStatement,
): { readonly package?: unknown; readonly version?: unknown } | undefined {
  const predicate = asRecord(statement.predicate);
  const buildDefinition = asRecord(predicate?.["buildDefinition"]);
  const externalParameters = asRecord(buildDefinition?.["externalParameters"]);
  return asRecord(externalParameters?.["releaseTarget"]);
}

function resolvedDependencyCommits(statement: InTotoStatement): readonly string[] {
  const predicate = asRecord(statement.predicate);
  const buildDefinition = asRecord(predicate?.["buildDefinition"]);
  const dependencies = buildDefinition?.["resolvedDependencies"];
  if (!Array.isArray(dependencies)) {
    return [];
  }
  const commits: string[] = [];
  for (const dependency of dependencies) {
    const record = asRecord(dependency);
    if (record?.["uri"] !== sourceRepo) {
      continue;
    }
    const digest = asRecord(record["digest"]);
    const gitCommit = digest?.["gitCommit"];
    if (typeof gitCommit === "string") {
      commits.push(gitCommit);
    }
  }
  return commits;
}

function builderIdFromStatement(statement: InTotoStatement): string | undefined {
  const predicate = asRecord(statement.predicate);
  const runDetails = asRecord(predicate?.["runDetails"]);
  const builder = asRecord(runDetails?.["builder"]);
  const id = builder?.["id"];
  return typeof id === "string" ? id : undefined;
}

function runCommand(
  input: CommandInput,
): Effect.Effect<import("./types.js").CommandResult, ReleaseError, ProcessProvider> {
  return Effect.gen(function* () {
    const processProvider = yield* ProcessProvider;
    return yield* processProvider.run(input);
  });
}

function stage<A, E extends ReleaseError, R>(
  name: string,
  message: string,
  effect: Effect.Effect<A, E, R>,
): Effect.Effect<A, E, R | LoggerProvider> {
  return Effect.gen(function* () {
    const logger = yield* LoggerProvider;
    const started = Date.now();
    yield* logger.log({ stage: name, status: "start", message });
    const exit = yield* Effect.exit(effect);
    const elapsedMs = Date.now() - started;
    if (Exit.isSuccess(exit)) {
      yield* logger.log({ stage: name, status: "ok", message, elapsedMs });
      return exit.value;
    }
    yield* logger.log({ stage: name, status: "fail", message, elapsedMs });
    return yield* Effect.failCause(exit.cause);
  });
}

function normalizeResolvedRef(
  requestedRef: string,
  stdout: string,
): Effect.Effect<string, ReleaseError> {
  const lines = stdout
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line !== "");
  const last = lines[lines.length - 1];
  if (last === undefined) {
    return Effect.fail(
      new VerificationFailed({
        reason: "oras resolve returned empty output",
        details: { requestedRef },
      }),
    );
  }
  if (/^[^@]+@sha256:[0-9a-f]{64}$/.test(last)) {
    return Effect.succeed(last);
  }
  if (/^sha256:[0-9a-f]{64}$/.test(last)) {
    return Effect.succeed(`${repositoryFromRef(requestedRef)}@${last}`);
  }
  return Effect.fail(
    new VerificationFailed({
      reason: "oras resolve did not return a digest reference",
      details: { requestedRef, stdout },
    }),
  );
}

function digestFromRef(ref: string): string {
  return /^.+@(sha256:[0-9a-f]{64})$/.exec(ref)?.[1] ?? "";
}

function parseJsonOutput(stdout: string): Effect.Effect<readonly unknown[], ReleaseError> {
  const trimmed = stdout.trim();
  if (trimmed === "") {
    return Effect.fail(
      new VerificationFailed({
        reason: "cosign verify-attestation returned empty output",
      }),
    );
  }
  try {
    return Effect.succeed([JSON.parse(trimmed) as unknown]);
  } catch {
    const values: unknown[] = [];
    for (const line of trimmed.split(/\r?\n/)) {
      const candidate = line.trim();
      if (candidate === "" || (!candidate.startsWith("{") && !candidate.startsWith("["))) {
        continue;
      }
      try {
        values.push(JSON.parse(candidate) as unknown);
      } catch (error) {
        return Effect.fail(
          new VerificationFailed({
            reason: "failed to parse cosign verify-attestation JSON output",
            details: { error: error instanceof Error ? error.message : String(error) },
          }),
        );
      }
    }
    return Effect.succeed(values);
  }
}

function defaultSdkVersionRef(version: string): string {
  return `${sdkOciRepository}:npm-v${version}`;
}

function repositoryFromRef(ref: string): string {
  const digestSeparator = ref.indexOf("@");
  if (digestSeparator >= 0) {
    return ref.slice(0, digestSeparator);
  }
  const lastSlash = ref.lastIndexOf("/");
  const lastColon = ref.lastIndexOf(":");
  if (lastColon > lastSlash) {
    return ref.slice(0, lastColon);
  }
  return ref;
}

function isReleaseTrack(value: string): value is ReleaseTrack {
  return trackValues.includes(value as ReleaseTrack);
}

function requireValue(args: readonly string[], index: number, flag: string): string {
  const value = args[index + 1];
  if (typeof value !== "string" || value === "" || value.startsWith("--")) {
    throw new ReleaseUsageError({ reason: `${flag} requires a value` });
  }
  return value;
}

function expect(
  condition: boolean,
  reason: string,
  details?: Readonly<Record<string, unknown>>,
): Effect.Effect<void, ReleaseError> {
  if (condition) {
    return Effect.void;
  }
  return Effect.fail(
    new AdmissionRejected(
      details === undefined
        ? { reason }
        : {
            reason,
            details,
          },
    ),
  );
}

function releaseErrorDetails(error: ReleaseError): Readonly<Record<string, unknown>> {
  if ("_tag" in error) {
    return {
      tag: error._tag,
      reason: "reason" in error ? error.reason : undefined,
      details: "details" in error ? error.details : undefined,
      stderr: "stderr" in error ? error.stderr : undefined,
      stdout: "stdout" in error ? error.stdout : undefined,
      exitCode: "exitCode" in error ? error.exitCode : undefined,
    };
  }
  return { error: String(error) };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return isRecord(value) ? value : undefined;
}

function isEntrypoint(): boolean {
  return process.argv[1] === fileURLToPath(import.meta.url);
}

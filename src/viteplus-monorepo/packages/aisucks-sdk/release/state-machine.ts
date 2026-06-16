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
  decodeStrictJson,
  decodeUnknown,
  encodeJson,
  fileJson,
  GithubOidcClaimsSchema,
  GithubOidcLogDetailsSchema,
  GithubOidcTokenResponseSchema,
  invalidJson,
  NpmOidcExchangeLogDetailsSchema,
  NpmOidcExchangeResponseSchema,
  NpmIntegrityViewSchema,
  PackageJsonSchema,
  ReleaseResultSchema,
  SdkOciResultSchema,
  verificationJson,
  type GithubOidcClaims,
} from "./schemas.js";
import {
  githubOidcIssuer,
  sdkPackageName,
  sdkReleaseWorkflowRef,
  sdkReleaseWorkflowIdentity,
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
const npmRegistry = "https://registry.npmjs.org/";

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
      config.createAttestation ? "create in-toto/SLSA statement" : "skip in-toto/SLSA statement",
      config.createAttestation
        ? createEvidenceBundle(candidate, outputDir)
        : Effect.succeed(undefined),
    );
    yield* stage(
      "admit",
      "validate candidate and evidence before public writes",
      admitRelease(config, candidate, evidence),
    );
    yield* stage(
      "npm-projection",
      "validate npm package projection before public writes",
      validateNpmProjection(config, candidate),
    );
    const publishedOci = yield* stage(
      "publish-oci",
      "publish admitted OCI subject",
      publishOci(config, candidate, outputDir),
    );
    const ociSignatureStatus = yield* stage(
      "sign-oci",
      "sign published OCI subject",
      signOci(config, publishedOci),
    );
    const ociAttestationStatus = yield* stage(
      "attest-oci",
      "attest published OCI subject",
      attestOci(config, publishedOci, evidence),
    );
    const npmStatus = yield* stage(
      "publish-npm",
      "publish npm package projection",
      publishNpm(config, candidate),
    );
    yield* stage(
      "verify",
      "verify final release projections",
      verifyRelease(
        config,
        candidate,
        publishedOci,
        ociSignatureStatus,
        ociAttestationStatus,
        npmStatus,
      ),
    );

    const files = yield* FileProvider;
    const logger = yield* LoggerProvider;
    const result: ReleaseResult = {
      target,
      candidate,
      ...(evidence === undefined ? {} : { evidence }),
      attestationStatus: evidence === undefined ? "not-requested" : "created",
      publishedOci,
      ociSignatureStatus,
      ociAttestationStatus,
      npmProvenanceStatus: config.npmProvenance ? "requested" : "not-requested",
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

function preflight(
  config: ReleaseConfig,
): Effect.Effect<void, ReleaseError, FileProvider | LoggerProvider> {
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
    if (config.mode === "publish" && config.publishNpm && !config.publishOci) {
      return yield* Effect.fail(
        new InvalidReleaseTarget({
          reason: "npm publication requires OCI publication",
          details: {
            package: sdkPackageName,
            ociRef: config.ociRef,
          },
        }),
      );
    }
    if (config.signOci && !config.publishOci) {
      return yield* Effect.fail(
        new InvalidReleaseTarget({
          reason: "OCI signing requires OCI publication",
        }),
      );
    }
    if (config.npmProvenance && !config.publishNpm) {
      return yield* Effect.fail(
        new InvalidReleaseTarget({
          reason: "npm provenance requires npm publication",
        }),
      );
    }
    if (config.mode === "publish" && (config.signOci || config.createAttestation)) {
      yield* requirePinnedReleaseWorkflowRef();
      yield* requireGitHubActionsOidcForOciSigning();
      yield* ociBasicAuth().pipe(Effect.asVoid);
    }
    if (config.mode === "publish" && config.publishNpm && process.env.GITHUB_ACTIONS === "true") {
      yield* requireGitHubActionsOidcForNpmPublish(sdkPackageName);
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

function requirePinnedReleaseWorkflowRef(): Effect.Effect<void, ReleaseError> {
  if (process.env.GITHUB_ACTIONS !== "true") {
    return Effect.void;
  }
  if (process.env.GITHUB_WORKFLOW_REF === sdkReleaseWorkflowRef) {
    return Effect.void;
  }
  return Effect.fail(
    new InvalidReleaseTarget({
      reason: "OCI signing requires the pinned npm SDK release workflow on main",
      details: {
        expectedWorkflowRef: sdkReleaseWorkflowRef,
        actualWorkflowRef: process.env.GITHUB_WORKFLOW_REF,
      },
    }),
  );
}

function requireGitHubActionsOidcForOciSigning(): Effect.Effect<void, ReleaseError> {
  if (
    process.env.GITHUB_ACTIONS === "true" &&
    process.env.ACTIONS_ID_TOKEN_REQUEST_URL !== undefined &&
    process.env.ACTIONS_ID_TOKEN_REQUEST_TOKEN !== undefined
  ) {
    return Effect.void;
  }
  return Effect.fail(
    new InvalidReleaseTarget({
      reason: "OCI signing requires GitHub Actions OIDC",
      details: {
        requiredPermission: "id-token: write",
        workflow: ".github/workflows/npm-sdk-release.yml",
      },
    }),
  );
}

function requireGitHubActionsOidcForNpmPublish(
  packageName: string,
): Effect.Effect<void, ReleaseError, LoggerProvider> {
  return Effect.gen(function* () {
    const requestUrl = process.env.ACTIONS_ID_TOKEN_REQUEST_URL;
    const requestToken = process.env.ACTIONS_ID_TOKEN_REQUEST_TOKEN;
    if (requestUrl === undefined || requestToken === undefined) {
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

    const url = new URL(requestUrl);
    url.searchParams.append("audience", "npm:registry.npmjs.org");
    const responseText = yield* Effect.tryPromise({
      try: async () => {
        const response = await fetch(url, {
          headers: {
            Accept: "application/json",
            Authorization: `Bearer ${requestToken}`,
          },
        });
        const body = await response.text();
        if (!response.ok) {
          throw new Error(`GitHub OIDC token request failed with HTTP ${response.status}: ${body}`);
        }
        return body;
      },
      catch: (error) =>
        new InvalidReleaseTarget({
          reason: "failed to fetch GitHub OIDC token for npm trusted publishing",
          details: { error: error instanceof Error ? error.message : String(error) },
        }),
    });
    const tokenResponse = yield* decodeJson(GithubOidcTokenResponseSchema, responseText, (reason) =>
      invalidJson("GitHub OIDC token response does not match release schema", { reason }),
    );
    const claims = yield* decodeGithubOidcClaims(tokenResponse.value);
    const claimDetails = yield* decodeUnknown(GithubOidcLogDetailsSchema, claims, (reason) =>
      invalidJson("GitHub OIDC claim details do not match release schema", { reason }),
    );
    const logger = yield* LoggerProvider;
    yield* logger.log({
      stage: "oidc",
      status: "ok",
      message: "observed GitHub OIDC claims for npm trusted publishing",
      details: claimDetails,
    });
    yield* validateNpmOidcExchange(packageName, tokenResponse.value);
  });
}

function decodeGithubOidcClaims(token: string): Effect.Effect<GithubOidcClaims, ReleaseError> {
  return Effect.gen(function* () {
    const payload = token.split(".")[1];
    if (payload === undefined || payload === "") {
      return yield* Effect.fail(
        new InvalidReleaseTarget({
          reason: "GitHub OIDC token is not a JWT",
        }),
      );
    }
    const claimsJson = Buffer.from(payload, "base64url").toString("utf8");
    return yield* decodeJson(GithubOidcClaimsSchema, claimsJson, (reason) =>
      invalidJson("GitHub OIDC token claims do not match release schema", { reason }),
    );
  });
}

function validateNpmOidcExchange(
  packageName: string,
  idToken: string,
): Effect.Effect<void, ReleaseError, LoggerProvider> {
  return Effect.gen(function* () {
    const exchangeUrl = new URL(
      `/-/npm/v1/oidc/token/exchange/package/${encodeURIComponent(packageName)}`,
      npmRegistry,
    );
    const responseText = yield* Effect.tryPromise({
      try: async () => {
        const response = await fetch(exchangeUrl, {
          method: "POST",
          headers: {
            Accept: "application/json",
            Authorization: `Bearer ${idToken}`,
          },
        });
        const body = await response.text();
        if (!response.ok) {
          throw new Error(`npm OIDC token exchange failed with HTTP ${response.status}: ${body}`);
        }
        return body;
      },
      catch: (error) =>
        new InvalidReleaseTarget({
          reason: "npm rejected GitHub OIDC token for trusted publishing",
          details: {
            packageName,
            registry: npmRegistry,
            error: error instanceof Error ? error.message : String(error),
          },
        }),
    });
    const exchange = yield* decodeJson(NpmOidcExchangeResponseSchema, responseText, (reason) =>
      invalidJson("npm OIDC token exchange response does not match release schema", { reason }),
    );
    const details = yield* decodeUnknown(
      NpmOidcExchangeLogDetailsSchema,
      {
        packageName,
        registry: npmRegistry,
        tokenIssued: exchange.token !== "",
      },
      (reason) => invalidJson("npm OIDC exchange details do not match release schema", { reason }),
    );
    const logger = yield* LoggerProvider;
    yield* logger.log({
      stage: "oidc",
      status: "ok",
      message: "npm accepted GitHub OIDC token for trusted publishing",
      details,
    });
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

function publishOci(
  config: ReleaseConfig,
  candidate: ReleaseCandidate,
  outputDir: string,
): Effect.Effect<SdkOciResult | undefined, ReleaseError, FileProvider | ProcessProvider> {
  if (!config.publishOci) {
    return Effect.succeed(undefined);
  }

  return Effect.gen(function* () {
    const resultPath = path.join(outputDir, "sdk-oci.public.json");
    const authArgs = yield* ociAuthArgs();

    yield* runSdkOci(config, [
      "--tarball",
      config.paths.tarball,
      "--pack-json",
      config.paths.packJson,
      "--ref",
      config.ociRef,
      "--source-commit",
      candidate.target.sourceCommit,
      "--output",
      resultPath,
      ...authArgs,
    ]);

    return yield* readSdkOciResult(resultPath);
  });
}

function ociAuthArgs(): Effect.Effect<readonly string[], ReleaseError> {
  const accessToken = process.env.GUARDIAN_OCI_ACCESS_TOKEN;
  if (accessToken !== undefined) {
    if (accessToken === "") {
      return Effect.fail(
        new InvalidReleaseTarget({
          reason: "GUARDIAN_OCI_ACCESS_TOKEN is set but empty",
        }),
      );
    }
    return Effect.succeed(["--access-token-env", "GUARDIAN_OCI_ACCESS_TOKEN"]);
  }

  const password = process.env.GUARDIAN_OCI_PASSWORD;
  if (password === undefined || password === "") {
    return Effect.fail(
      new InvalidReleaseTarget({
        reason: "OCI publication requires GUARDIAN_OCI_PASSWORD or GUARDIAN_OCI_ACCESS_TOKEN",
      }),
    );
  }

  const username = process.env.GUARDIAN_OCI_USERNAME ?? "guardian-release";
  if (username === "") {
    return Effect.fail(
      new InvalidReleaseTarget({
        reason: "GUARDIAN_OCI_USERNAME is set but empty",
      }),
    );
  }
  return Effect.succeed(["--username", username, "--password-env", "GUARDIAN_OCI_PASSWORD"]);
}

function ociBasicAuth(): Effect.Effect<
  { readonly username: string; readonly password: string },
  ReleaseError
> {
  if (process.env.GUARDIAN_OCI_ACCESS_TOKEN !== undefined) {
    return Effect.fail(
      new InvalidReleaseTarget({
        reason:
          "cosign OCI signing requires GUARDIAN_OCI_USERNAME/GUARDIAN_OCI_PASSWORD; bearer token signing is not wired",
      }),
    );
  }

  const password = process.env.GUARDIAN_OCI_PASSWORD;
  if (password === undefined || password === "") {
    return Effect.fail(
      new InvalidReleaseTarget({
        reason: "OCI signing requires GUARDIAN_OCI_PASSWORD",
      }),
    );
  }

  const username = process.env.GUARDIAN_OCI_USERNAME ?? "guardian-release";
  if (username === "") {
    return Effect.fail(
      new InvalidReleaseTarget({
        reason: "GUARDIAN_OCI_USERNAME is set but empty",
      }),
    );
  }
  return Effect.succeed({ username, password });
}

function signOci(
  config: ReleaseConfig,
  publishedOci: SdkOciResult | undefined,
): Effect.Effect<"not-requested" | "signed", ReleaseError, LoggerProvider | ProcessProvider> {
  if (publishedOci === undefined) {
    return Effect.succeed("not-requested");
  }
  if (!config.signOci) {
    return Effect.succeed("not-requested");
  }

  return signPublishedOci(config, publishedOci).pipe(
    Effect.catchAll((signingError) =>
      rollbackUnsignedOci(config, publishedOci).pipe(
        Effect.matchEffect({
          onFailure: (cleanupError) =>
            Effect.fail(
              new VerificationFailed({
                reason: "OCI signing failed and unsigned OCI rollback failed",
                details: {
                  ociRef: publishedOci.oci_ref,
                  signingError: releaseErrorSummary(signingError),
                  cleanupError: releaseErrorSummary(cleanupError),
                },
              }),
            ),
          onSuccess: () => Effect.fail(signingError),
        }),
      ),
    ),
  );
}

function signPublishedOci(
  config: ReleaseConfig,
  publishedOci: SdkOciResult,
): Effect.Effect<"signed", ReleaseError, ProcessProvider> {
  return Effect.gen(function* () {
    yield* loginCosignRegistry(config, publishedOci.oci_ref);
    yield* retryTransient(
      runCommand({
        program: config.paths.cosign,
        args: cosignSignArgs(publishedOci.oci_ref),
        cwd: config.paths.repoRoot,
        env: cosignReleaseEnv(),
        timeoutMs: publishTimeoutMs,
      }),
      isTransientReleaseError,
    );
    return signedOciStatus();
  });
}

function attestOci(
  config: ReleaseConfig,
  publishedOci: SdkOciResult | undefined,
  evidence: EvidenceBundle | undefined,
): Effect.Effect<"not-requested" | "attested", ReleaseError, ProcessProvider> {
  if (publishedOci === undefined || evidence === undefined) {
    return Effect.succeed("not-requested");
  }

  return attestPublishedOci(config, publishedOci, evidence);
}

function attestPublishedOci(
  config: ReleaseConfig,
  publishedOci: SdkOciResult,
  evidence: EvidenceBundle,
): Effect.Effect<"attested", ReleaseError, ProcessProvider> {
  return Effect.gen(function* () {
    yield* loginCosignRegistry(config, publishedOci.oci_ref);
    yield* retryTransient(
      runCommand({
        program: config.paths.cosign,
        args: cosignAttestArgs(publishedOci.oci_ref, evidence.statementPath),
        cwd: config.paths.repoRoot,
        env: cosignReleaseEnv(),
        timeoutMs: publishTimeoutMs,
      }),
      isTransientReleaseError,
    );
    return "attested" as const;
  });
}

function loginCosignRegistry(
  config: ReleaseConfig,
  ociRef: string,
): Effect.Effect<void, ReleaseError, ProcessProvider> {
  return Effect.gen(function* () {
    const auth = yield* ociBasicAuth();
    const registry = ociRegistryFromRef(ociRef);
    yield* retryTransient(
      runCommand({
        program: config.paths.cosign,
        args: ["login", registry, "--username", auth.username, "--password-stdin"],
        redactedArgs: ["login", registry, "--username", auth.username, "--password-stdin"],
        stdin: `${auth.password}\n`,
        cwd: config.paths.repoRoot,
        env: cosignReleaseEnv(),
        timeoutMs: commandTimeoutMs,
      }),
      isTransientReleaseError,
    );
  });
}

function rollbackUnsignedOci(
  config: ReleaseConfig,
  publishedOci: SdkOciResult,
): Effect.Effect<void, ReleaseError, LoggerProvider | ProcessProvider> {
  return Effect.gen(function* () {
    const logger = yield* LoggerProvider;
    const started = Date.now();
    yield* logger.log({
      stage: "rollback-oci",
      status: "start",
      message: "delete unsigned OCI subject after signing failure",
      details: {
        ociRef: publishedOci.oci_ref,
      },
    });
    yield* deleteOciRef(config, publishedOci.oci_ref).pipe(
      Effect.tap(() =>
        logger.log({
          stage: "rollback-oci",
          status: "ok",
          message: "delete unsigned OCI subject after signing failure",
          elapsedMs: Date.now() - started,
        }),
      ),
      Effect.tapError((error) =>
        logger.log({
          stage: "rollback-oci",
          status: "fail",
          message: "delete unsigned OCI subject after signing failure",
          elapsedMs: Date.now() - started,
          details: { tag: error._tag },
        }),
      ),
    );
  });
}

function deleteOciRef(
  config: ReleaseConfig,
  ref: string | undefined,
): Effect.Effect<void, ReleaseError, ProcessProvider> {
  if (ref === undefined) {
    return Effect.void;
  }

  return Effect.gen(function* () {
    const auth = yield* ociBasicAuth();
    yield* retryTransient(
      runCommand({
        program: config.paths.oras,
        args: ociManifestDeleteArgs(ref, auth.username),
        redactedArgs: ociManifestDeleteArgs(ref, auth.username),
        stdin: `${auth.password}\n`,
        cwd: config.paths.repoRoot,
        timeoutMs: commandTimeoutMs,
      }),
      isTransientReleaseError,
    );
  });
}

export function ociManifestDeleteArgs(ref: string, username: string): readonly string[] {
  return ["manifest", "delete", "--force", "--username", username, "--password-stdin", ref];
}

function releaseErrorSummary(error: ReleaseError): Readonly<Record<string, unknown>> {
  const summary: Record<string, unknown> = { tag: error._tag };
  if ("reason" in error) {
    summary.reason = error.reason;
  }
  if ("program" in error) {
    summary.program = error.program;
  }
  if ("exitCode" in error) {
    summary.exitCode = error.exitCode;
  }
  if ("stderr" in error && error.stderr !== "") {
    summary.stderr = error.stderr.slice(0, 500);
  }
  return summary;
}

function signedOciStatus(): "signed" {
  return "signed";
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
        yield* ensureNpmDistTag(config, candidate);
        return "already-published";
      }
      return yield* Effect.fail(npmIntegrityConflict(candidate, existing));
    }

    const publishArgs = [
      "publish",
      config.paths.tarball,
      "--tag",
      candidate.target.channel,
      "--access",
      "public",
      "--loglevel",
      "verbose",
    ];
    if (config.npmProvenance) {
      publishArgs.push("--provenance");
    }
    yield* retryTransient(runNpm(config, publishArgs, publishTimeoutMs), isTransientReleaseError);
    yield* ensureNpmDistTag(config, candidate);
    return "published";
  });
}

function ensureNpmDistTag(
  config: ReleaseConfig,
  candidate: ReleaseCandidate,
): Effect.Effect<void, ReleaseError, ProcessProvider> {
  return retryTransient(
    runNpm(
      config,
      [
        "dist-tag",
        "add",
        `${candidate.pack.name}@${candidate.pack.version}`,
        candidate.target.channel,
      ],
      publishTimeoutMs,
    ),
    isTransientReleaseError,
  ).pipe(Effect.asVoid);
}

export function validateNpmProjection(
  config: ReleaseConfig,
  candidate: ReleaseCandidate,
): Effect.Effect<
  "not-requested" | "available" | "already-published",
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
    if (existing === undefined) {
      return "available";
    }
    if (existing === candidate.npmIntegrity) {
      return "already-published";
    }
    return yield* Effect.fail(npmIntegrityConflict(candidate, existing));
  });
}

function npmIntegrityConflict(candidate: ReleaseCandidate, existing: string): PublishConflict {
  return new PublishConflict({
    reason: "npm version already exists with different integrity",
    details: {
      package: candidate.pack.name,
      version: candidate.pack.version,
      expected: candidate.npmIntegrity,
      actual: existing,
    },
  });
}

function ociRegistryFromRef(ref: string): string {
  const firstSlash = ref.indexOf("/");
  return firstSlash < 0 ? ref : ref.slice(0, firstSlash);
}

function verifyRelease(
  config: ReleaseConfig,
  candidate: ReleaseCandidate,
  publishedOci: SdkOciResult | undefined,
  ociSignatureStatus: "not-requested" | "signed",
  ociAttestationStatus: "not-requested" | "attested",
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
      if (ociSignatureStatus === "signed") {
        yield* verifyOciSignature(config, publishedOci);
      }
      if (ociAttestationStatus === "attested") {
        yield* verifyOciAttestation(config, publishedOci);
      }
    }

    if (npmStatus !== "not-requested") {
      const integrity = yield* npmViewIntegrity(config, candidate.pack, { retryNotFound: true });
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

function verifyOciSignature(
  config: ReleaseConfig,
  publishedOci: SdkOciResult,
): Effect.Effect<void, ReleaseError, ProcessProvider> {
  return retryTransient(
    runCommand({
      program: config.paths.cosign,
      args: cosignVerifyArgs(publishedOci.oci_ref),
      cwd: config.paths.repoRoot,
      env: cosignReleaseEnv(),
      timeoutMs: commandTimeoutMs,
    }),
    isTransientReleaseError,
  ).pipe(Effect.asVoid);
}

function verifyOciAttestation(
  config: ReleaseConfig,
  publishedOci: SdkOciResult,
): Effect.Effect<void, ReleaseError, ProcessProvider> {
  return retryTransient(
    runCommand({
      program: config.paths.cosign,
      args: cosignVerifyAttestationArgs(publishedOci.oci_ref),
      cwd: config.paths.repoRoot,
      env: cosignReleaseEnv(),
      timeoutMs: commandTimeoutMs,
    }),
    isTransientReleaseError,
  ).pipe(Effect.asVoid);
}

export function npmViewIntegrity(
  config: ReleaseConfig,
  pack: NpmPackEntry,
  options: {
    readonly retryNotFound?: boolean;
    readonly attempts?: number;
    readonly delayMs?: number;
  } = {},
): Effect.Effect<string, ReleaseError, ProcessProvider> {
  const retryNotFound = options.retryNotFound === true;
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
    (error) =>
      isTransientReleaseError(error) ||
      (retryNotFound && error._tag === "CommandFailed" && isNpmNotFound(releaseErrorText(error))),
    options.attempts ?? (retryNotFound ? 12 : 3),
    options.delayMs ?? (retryNotFound ? 1_000 : 500),
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
    env: npmReleaseEnv(config.npmProvenance),
    timeoutMs,
  });
}

export function npmReleaseEnv(provenance = false): Readonly<Record<string, string>> {
  const env: Record<string, string> = {
    NPM_CONFIG_AUDIT: "false",
    NPM_CONFIG_FUND: "false",
    NPM_CONFIG_REGISTRY: npmRegistry,
  };
  if (provenance) {
    env.NPM_CONFIG_PROVENANCE = "true";
  }
  return env;
}

export function cosignReleaseEnv(): Readonly<Record<string, string>> {
  return {};
}

export function cosignSignArgs(ociRef: string): readonly string[] {
  return ["sign", "--yes", "--oidc-provider", "github-actions", ociRef];
}

export function cosignAttestArgs(ociRef: string, statementPath: string): readonly string[] {
  return [
    "attest",
    "--yes",
    "--oidc-provider",
    "github-actions",
    "--type",
    "slsaprovenance1",
    "--statement",
    statementPath,
    ociRef,
  ];
}

export function cosignVerifyArgs(ociRef: string): readonly string[] {
  return [
    "verify",
    ociRef,
    "--certificate-identity",
    sdkReleaseWorkflowIdentity,
    "--certificate-oidc-issuer",
    githubOidcIssuer,
  ];
}

export function cosignVerifyAttestationArgs(ociRef: string): readonly string[] {
  return [
    "verify-attestation",
    "--type",
    "slsaprovenance1",
    ociRef,
    "--certificate-identity",
    sdkReleaseWorkflowIdentity,
    "--certificate-oidc-issuer",
    githubOidcIssuer,
  ];
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
    return yield* decodeStrictJson(SdkOciResultSchema, raw.toString("utf8"), (reason) =>
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
  return (
    stderr.includes("E404") ||
    stderr.includes("404 Not Found") ||
    stderr.includes("No match found for version")
  );
}

function releaseErrorText(error: ReleaseError): string {
  if (error._tag !== "CommandFailed" && error._tag !== "CommandTimedOut") {
    return "";
  }
  return `${error.stderr}\n${error.stdout}`;
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

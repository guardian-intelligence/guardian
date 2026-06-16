import assert from "node:assert/strict";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { Cause, Effect, Exit, Layer, Option } from "effect";

import { declareRelease, parseDeclarationConfig } from "./declare.js";
import { CommandFailed } from "./errors.js";
import { LoggerProvider, NodeFileLayer, ProcessProvider, type CommandInput } from "./providers.js";
import {
  cosignAttestArgs,
  cosignReleaseEnv,
  cosignSignArgs,
  cosignVerifyArgs,
  cosignVerifyAttestationArgs,
  npmViewIntegrity,
  npmReleaseEnv,
  ociManifestDeleteArgs,
  validateNpmProjection,
} from "./state-machine.js";
import {
  defaultChannel,
  defaultOciRef,
  defaultReleasePaths,
  distributable,
  githubOidcIssuer,
  inTotoStatementType,
  payloadForm,
  sdkPackageName,
  sdkReleaseWorkflowIdentity,
  slsaProvenancePredicateType,
  sourceRepo,
  type CommandResult,
  type InTotoStatement,
  type ReleaseCandidate,
  type ReleaseConfig,
} from "./types.js";

void test("npm release commands always target the public npm registry", () => {
  assert.deepEqual(npmReleaseEnv(), {
    NPM_CONFIG_AUDIT: "false",
    NPM_CONFIG_FUND: "false",
    NPM_CONFIG_REGISTRY: "https://registry.npmjs.org/",
  });
});

void test("npm provenance is opt-in for this release milestone", () => {
  assert.deepEqual(npmReleaseEnv(true), {
    NPM_CONFIG_AUDIT: "false",
    NPM_CONFIG_FUND: "false",
    NPM_CONFIG_PROVENANCE: "true",
    NPM_CONFIG_REGISTRY: "https://registry.npmjs.org/",
  });
});

void test("cosign release commands use stock v3 defaults", () => {
  assert.deepEqual(cosignReleaseEnv(), {});
});

void test("cosign OCI signing uses stock keyless v3 arguments", () => {
  const ref =
    "oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:46b6566ec4c6320cfcebdfd76f2e2d07ec315e681a1f5edf5ed693c32abac8b8";
  assert.deepEqual(cosignSignArgs(ref), [
    "sign",
    "--yes",
    "--oidc-provider",
    "github-actions",
    ref,
  ]);
});

void test("cosign OCI attestation uses stock keyless in-toto arguments", () => {
  const ref =
    "oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:46b6566ec4c6320cfcebdfd76f2e2d07ec315e681a1f5edf5ed693c32abac8b8";
  assert.deepEqual(cosignAttestArgs(ref, "/tmp/aisucks-sdk.slsa-provenance.json"), [
    "attest",
    "--yes",
    "--oidc-provider",
    "github-actions",
    "--type",
    "slsaprovenance1",
    "--statement",
    "/tmp/aisucks-sdk.slsa-provenance.json",
    ref,
  ]);
});

void test("cosign verification pins the release workflow identity", () => {
  const ref =
    "oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:46b6566ec4c6320cfcebdfd76f2e2d07ec315e681a1f5edf5ed693c32abac8b8";
  assert.deepEqual(cosignVerifyArgs(ref), [
    "verify",
    ref,
    "--certificate-identity",
    "https://github.com/guardian-intelligence/guardian/.github/workflows/npm-sdk-release.yml@refs/heads/main",
    "--certificate-oidc-issuer",
    "https://token.actions.githubusercontent.com",
  ]);
  assert.deepEqual(cosignVerifyAttestationArgs(ref), [
    "verify-attestation",
    "--type",
    "slsaprovenance1",
    ref,
    "--certificate-identity",
    "https://github.com/guardian-intelligence/guardian/.github/workflows/npm-sdk-release.yml@refs/heads/main",
    "--certificate-oidc-issuer",
    "https://token.actions.githubusercontent.com",
  ]);
});

void test("release declaration admits a verified public SDK OCI subject", async (t) => {
  const outputDir = mkdtempSync(path.join(os.tmpdir(), "guardian-declare-test-"));
  t.after(() => rmSync(outputDir, { force: true, recursive: true }));
  const calls: CommandInput[] = [];
  const config = parseDeclarationConfig([
    "--product",
    "aisucks",
    "--version",
    declarationVersion,
    "--commit",
    declarationCommit,
    "--track",
    "rc",
    "--output-dir",
    outputDir,
    "--cosign",
    "cosign",
    "--oras",
    "oras",
  ]);

  const result = await Effect.runPromise(
    declareRelease(config).pipe(
      Effect.provide(
        declarationLayer(calls, {
          attestationStdout: declarationDsseOutput(declarationStatement()),
        }),
      ),
    ),
  );

  assert.equal(result.subjectRef, declarationDigestRef);
  assert.equal(result.subjectDigest, declarationDigest);
  assert.equal(existsSync(path.join(outputDir, "release-declaration.json")), true);
  assert.deepEqual(
    calls.map((call) => call.args),
    [
      ["resolve", "--full-reference", declarationVersionRef],
      [
        "verify",
        declarationDigestRef,
        "--certificate-identity",
        sdkReleaseWorkflowIdentity,
        "--certificate-oidc-issuer",
        githubOidcIssuer,
      ],
      [
        "verify-attestation",
        "--type",
        "slsaprovenance1",
        declarationDigestRef,
        "--certificate-identity",
        sdkReleaseWorkflowIdentity,
        "--certificate-oidc-issuer",
        githubOidcIssuer,
      ],
    ],
  );
});

void test("release declaration rejects when cosign cannot verify the SLSA attestation contract", async () => {
  const calls: CommandInput[] = [];
  const exit = await Effect.runPromiseExit(
    declareRelease(
      parseDeclarationConfig([
        "--product",
        "aisucks",
        "--version",
        declarationVersion,
        "--commit",
        declarationCommit,
        "--track",
        "rc",
      ]),
    ).pipe(
      Effect.provide(
        declarationLayer(calls, {
          attestationFailure: "no matching attestations for certificate identity",
        }),
      ),
    ),
  );

  const failure = releaseFailure(exit);
  assert.equal(failure._tag, "VerificationFailed");
  if (failure._tag === "VerificationFailed") {
    assert.equal(failure.reason, "declared OCI SLSA attestation verification failed");
  }
});

void test("release declaration rejects a verified SLSA statement for the wrong source commit", async () => {
  const calls: CommandInput[] = [];
  const exit = await Effect.runPromiseExit(
    declareRelease(
      parseDeclarationConfig([
        "--product",
        "aisucks",
        "--version",
        declarationVersion,
        "--commit",
        declarationCommit,
        "--track",
        "rc",
      ]),
    ).pipe(
      Effect.provide(
        declarationLayer(calls, {
          attestationStdout: declarationDsseOutput(
            declarationStatement({
              commit: "cccccccccccccccccccccccccccccccccccccccc",
            }),
          ),
        }),
      ),
    ),
  );

  const failure = releaseFailure(exit);
  assert.equal(failure._tag, "AdmissionRejected");
  if (failure._tag === "AdmissionRejected") {
    assert.equal(failure.reason, "SLSA provenance source commit mismatch");
  }
});

void test("OCI rollback deletes the exact manifest ref with password on stdin", () => {
  assert.deepEqual(
    ociManifestDeleteArgs(
      "oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:46b6566ec4c6320cfcebdfd76f2e2d07ec315e681a1f5edf5ed693c32abac8b8",
      "guardian-release",
    ),
    [
      "manifest",
      "delete",
      "--force",
      "--username",
      "guardian-release",
      "--password-stdin",
      "oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:46b6566ec4c6320cfcebdfd76f2e2d07ec315e681a1f5edf5ed693c32abac8b8",
    ],
  );
});

void test("validateNpmProjection accepts an already-published matching package", async () => {
  const candidate = releaseCandidate();
  const got = await Effect.runPromise(
    validateNpmProjection(releaseConfig(), candidate).pipe(
      Effect.provide(npmViewLayer(JSON.stringify(candidate.npmIntegrity))),
    ),
  );

  assert.equal(got, "already-published");
});

void test("validateNpmProjection treats npm 404 as available", async () => {
  const got = await Effect.runPromise(
    validateNpmProjection(releaseConfig(), releaseCandidate()).pipe(
      Effect.provide(npmNotFoundLayer()),
    ),
  );

  assert.equal(got, "available");
});

void test("validateNpmProjection rejects an existing package with different integrity", async () => {
  const candidate = releaseCandidate();
  const exit = await Effect.runPromiseExit(
    validateNpmProjection(releaseConfig(), candidate).pipe(
      Effect.provide(npmViewLayer(JSON.stringify("sha512-different"))),
    ),
  );

  assert.equal(Exit.isFailure(exit), true);
  if (Exit.isSuccess(exit)) {
    return;
  }
  const failure = Cause.failureOption(exit.cause);
  assert.equal(Option.isSome(failure), true);
  if (Option.isNone(failure)) {
    return;
  }
  assert.equal(failure.value._tag, "PublishConflict");
  if (failure.value._tag !== "PublishConflict") {
    return;
  }
  assert.equal(failure.value.reason, "npm version already exists with different integrity");
});

void test("npm final verification retries registry 404 propagation", async () => {
  const candidate = releaseCandidate();
  let calls = 0;

  const got = await Effect.runPromise(
    npmViewIntegrity(releaseConfig(), candidate.pack, {
      retryNotFound: true,
      attempts: 2,
      delayMs: 0,
    }).pipe(
      Effect.provide(
        Layer.succeed(ProcessProvider, {
          run: (input: CommandInput) => {
            calls += 1;
            if (calls === 1) {
              return Effect.fail(
                new CommandFailed({
                  program: input.program,
                  args: input.args,
                  cwd: input.cwd,
                  exitCode: 1,
                  stdout: '{ "error": { "summary": "No match found for version 0.3.0" } }',
                  stderr: "npm error code E404",
                }),
              );
            }
            return Effect.succeed({
              program: input.program,
              args: input.args,
              cwd: input.cwd,
              exitCode: 0,
              stdout: JSON.stringify(candidate.npmIntegrity),
              stderr: "",
              durationMs: 1,
            });
          },
        }),
      ),
    ),
  );

  assert.equal(got, candidate.npmIntegrity);
  assert.equal(calls, 2);
});

function releaseConfig(): ReleaseConfig {
  return {
    mode: "publish",
    version: "0.3.0",
    channel: defaultChannel,
    ociRef: defaultOciRef,
    publishNpm: true,
    publishOci: true,
    createAttestation: false,
    signOci: false,
    npmProvenance: false,
    allowUnsignedDev: false,
    outputDir: undefined,
    paths: defaultReleasePaths(),
  };
}

function releaseCandidate(): ReleaseCandidate {
  const integrity = "sha512-match";
  return {
    target: {
      packageName: sdkPackageName,
      version: "0.3.0",
      channel: defaultChannel,
      sourceRepo,
      sourceCommit: "7699c753ec8a4338078b28c92941f5b4875782e6",
      ociRef: defaultOciRef,
    },
    pack: {
      name: sdkPackageName,
      version: "0.3.0",
      filename: "guardian-intelligence-aisucks-0.3.0.tgz",
      integrity,
      size: 1024,
    },
    oci: {
      distributable,
      payload_form: payloadForm,
      channel: defaultChannel,
      oci_digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      oci_ref:
        "oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      payload_sha256: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      tarball_sha256: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      npm_integrity: integrity,
      package: sdkPackageName,
      version: "0.3.0",
      source_repo: sourceRepo,
      source_commit: "7699c753ec8a4338078b28c92941f5b4875782e6",
      layer_title: "guardian-intelligence-aisucks-0.3.0.tgz",
    },
    tarballSha256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    npmIntegrity: integrity,
    localLayout: "/tmp/guardian-sdk-release/oci-layout",
  };
}

function npmViewLayer(stdout: string): Layer.Layer<ProcessProvider> {
  return Layer.succeed(ProcessProvider, {
    run: (input: CommandInput) =>
      Effect.succeed({
        program: input.program,
        args: input.args,
        cwd: input.cwd,
        exitCode: 0,
        stdout,
        stderr: "",
        durationMs: 1,
      }),
  });
}

function npmNotFoundLayer(): Layer.Layer<ProcessProvider> {
  return Layer.succeed(ProcessProvider, {
    run: (input: CommandInput) =>
      Effect.fail(
        new CommandFailed({
          program: input.program,
          args: input.args,
          cwd: input.cwd,
          exitCode: 1,
          stdout: "",
          stderr: "npm ERR! code E404\nnpm ERR! 404 Not Found",
        }),
      ),
  });
}

const declarationVersion = "1.2.3-rc.1";
const declarationCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";
const declarationDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
const declarationDigestRef = `oci.guardianintelligence.org/guardian/aisucks/sdk/npm@${declarationDigest}`;
const declarationVersionRef = `oci.guardianintelligence.org/guardian/aisucks/sdk/npm:npm-v${declarationVersion}`;
const declarationBuilderId =
  "https://github.com/guardian-intelligence/guardian/.github/workflows/npm-sdk-release.yml";

function declarationStatement(
  overrides: {
    readonly commit?: string;
    readonly version?: string;
    readonly subjectDigest?: string;
  } = {},
): InTotoStatement {
  return {
    _type: inTotoStatementType,
    subject: [
      {
        name: declarationDigestRef,
        digest: {
          sha256: (overrides.subjectDigest ?? declarationDigest).replace(/^sha256:/, ""),
        },
      },
    ],
    predicateType: slsaProvenancePredicateType,
    predicate: {
      buildDefinition: {
        externalParameters: {
          releaseTarget: {
            package: sdkPackageName,
            version: overrides.version ?? declarationVersion,
          },
        },
        resolvedDependencies: [
          {
            uri: sourceRepo,
            digest: {
              gitCommit: overrides.commit ?? declarationCommit,
            },
          },
        ],
      },
      runDetails: {
        builder: {
          id: declarationBuilderId,
        },
      },
    },
  };
}

function declarationDsseOutput(statement: InTotoStatement): string {
  return `${JSON.stringify({
    payload: Buffer.from(JSON.stringify(statement), "utf8").toString("base64"),
  })}\n`;
}

function declarationLayer(
  calls: CommandInput[],
  options: {
    readonly attestationStdout?: string;
    readonly attestationFailure?: string;
  },
) {
  return Layer.mergeAll(
    NodeFileLayer,
    Layer.succeed(LoggerProvider, {
      log: () => Effect.void,
      events: () => [],
    }),
    Layer.succeed(ProcessProvider, {
      run: (input: CommandInput) => {
        calls.push(input);
        if (input.program === "oras") {
          return Effect.succeed(commandResult(input, `${declarationDigestRef}\n`));
        }
        if (input.args[0] === "verify") {
          return Effect.succeed(commandResult(input, ""));
        }
        if (input.args[0] === "verify-attestation") {
          if (options.attestationFailure !== undefined) {
            return Effect.fail(
              new CommandFailed({
                program: input.program,
                args: input.args,
                cwd: input.cwd,
                exitCode: 1,
                stdout: "",
                stderr: options.attestationFailure,
              }),
            );
          }
          return Effect.succeed(commandResult(input, options.attestationStdout ?? ""));
        }
        return Effect.fail(
          new CommandFailed({
            program: input.program,
            args: input.args,
            cwd: input.cwd,
            exitCode: 127,
            stdout: "",
            stderr: `unexpected command: ${input.program} ${input.args.join(" ")}`,
          }),
        );
      },
    }),
  );
}

function commandResult(input: CommandInput, stdout: string): CommandResult {
  return {
    program: input.program,
    args: input.args,
    cwd: input.cwd,
    exitCode: 0,
    stdout,
    stderr: "",
    durationMs: 1,
  };
}

function releaseFailure(exit: Exit.Exit<unknown, unknown>): {
  readonly _tag: string;
  readonly reason?: string;
} {
  assert.equal(Exit.isFailure(exit), true);
  if (Exit.isSuccess(exit)) {
    throw new Error("expected failure");
  }
  const failure = Cause.failureOption(exit.cause);
  assert.equal(Option.isSome(failure), true);
  if (Option.isNone(failure)) {
    throw new Error("expected typed failure");
  }
  return failure.value as { readonly _tag: string; readonly reason?: string };
}

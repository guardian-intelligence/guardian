import assert from "node:assert/strict";
import test from "node:test";

import { Cause, Effect, Exit, Layer, Option } from "effect";

import { CommandFailed } from "./errors.js";
import { ProcessProvider, type CommandInput } from "./providers.js";
import {
  cosignReleaseEnv,
  npmOidcUserconfigContent,
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
  payloadForm,
  sdkPackageName,
  sourceRepo,
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

void test("npm OIDC userconfig points release mutations at the public registry", () => {
  assert.equal(
    npmOidcUserconfigContent("npm_oidc_token"),
    [
      "registry=https://registry.npmjs.org/",
      "//registry.npmjs.org/:_authToken=npm_oidc_token",
      "",
    ].join("\n"),
  );
});

void test("cosign release commands opt into OCI referrers support", () => {
  assert.deepEqual(cosignReleaseEnv(), {
    COSIGN_EXPERIMENTAL: "1",
  });
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

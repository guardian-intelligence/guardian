import assert from "node:assert/strict";
import test from "node:test";

import { cosignReleaseEnv, npmReleaseEnv, ociManifestDeleteArgs } from "./state-machine.js";

void test("npm release commands always target the public npm registry", () => {
  assert.deepEqual(npmReleaseEnv(), {
    NPM_CONFIG_AUDIT: "false",
    NPM_CONFIG_FUND: "false",
    NPM_CONFIG_PROVENANCE: "true",
    NPM_CONFIG_REGISTRY: "https://registry.npmjs.org/",
  });
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

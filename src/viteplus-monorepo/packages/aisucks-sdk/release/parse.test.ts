import assert from "node:assert/strict";
import path from "node:path";
import test from "node:test";

import { parseReleaseConfig } from "./parse.js";

void test("parseReleaseConfig rebases release paths under source root", () => {
  const sourceRoot = path.resolve("/tmp/guardian-release-source");
  const config = parseReleaseConfig(
    ["--publish", "--skip-npm", "--source-root", sourceRoot, "--sdkoci", "/tmp/sdkoci"],
    "0.3.0",
  );

  assert.equal(config.mode, "publish");
  assert.equal(config.publishNpm, false);
  assert.equal(config.publishOci, true);
  assert.equal(config.createAttestation, false);
  assert.equal(config.signOci, false);
  assert.equal(config.npmProvenance, false);
  assert.equal(config.paths.repoRoot, sourceRoot);
  assert.equal(
    config.paths.packageRoot,
    path.join(sourceRoot, "src/viteplus-monorepo/packages/aisucks-sdk"),
  );
  assert.equal(config.paths.sdkoci, path.resolve("/tmp/sdkoci"));
  assert.equal(
    config.paths.tarball,
    path.join(sourceRoot, "bazel-bin/src/viteplus-monorepo/packages/aisucks-sdk/aisucks-sdk.tgz"),
  );
  assert.equal(
    config.paths.packJson,
    path.join(
      sourceRoot,
      "bazel-bin/src/viteplus-monorepo/packages/aisucks-sdk/aisucks-sdk.npm-pack.json",
    ),
  );
});

void test("parseReleaseConfig keeps public provenance surfaces opt-in", () => {
  const config = parseReleaseConfig(
    ["--publish", "--with-attestation", "--sign-oci", "--npm-provenance"],
    "0.3.0",
  );

  assert.equal(config.createAttestation, true);
  assert.equal(config.signOci, true);
  assert.equal(config.npmProvenance, true);
});

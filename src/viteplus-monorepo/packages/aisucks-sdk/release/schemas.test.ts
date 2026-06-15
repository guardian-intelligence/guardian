import assert from "node:assert/strict";
import test from "node:test";

import { decodeStrictJsonSync, SdkOciResultSchema } from "./schemas.js";

const manifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
const payloadDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd";
const attestationDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";

function validSdkOciResult(): Record<string, unknown> {
  return {
    distributable: "aisucks-ts-sdk",
    payload_form: "npm",
    channel: "edge",
    oci_digest: manifestDigest,
    oci_ref: `oci.guardianintelligence.org/guardian/aisucks/sdk/npm@${manifestDigest}`,
    payload_sha256: payloadDigest,
    tarball_sha256: payloadDigest,
    npm_integrity: `sha512-${Buffer.alloc(64).toString("base64")}`,
    package: "@guardian-intelligence/aisucks",
    version: "0.3.0",
    source_repo: "https://github.com/guardian-intelligence/guardian",
    source_commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    layer_title: "guardian-intelligence-aisucks-0.3.0.tgz",
  };
}

function decodeSdkOciResult(input: unknown): unknown {
  return decodeStrictJsonSync(SdkOciResultSchema, JSON.stringify(input));
}

function assertRejectsSdkOciResult(input: unknown): void {
  assert.throws(() => decodeSdkOciResult(input));
}

test("SdkOciResultSchema accepts a valid npm OCI result", () => {
  assert.deepEqual(decodeSdkOciResult(validSdkOciResult()), validSdkOciResult());
});

test("SdkOciResultSchema rejects OCI refs without a repository", () => {
  assertRejectsSdkOciResult({
    ...validSdkOciResult(),
    oci_ref: `@${manifestDigest}`,
  });
});

test("SdkOciResultSchema rejects OCI refs with multiple digest separators", () => {
  assertRejectsSdkOciResult({
    ...validSdkOciResult(),
    oci_ref: `oci.guardianintelligence.org/guardian/aisucks/sdk/npm@${attestationDigest}@${manifestDigest}`,
  });
});

test("SdkOciResultSchema rejects OCI ref and digest mismatch", () => {
  assertRejectsSdkOciResult({
    ...validSdkOciResult(),
    oci_ref: `oci.guardianintelligence.org/guardian/aisucks/sdk/npm@${attestationDigest}`,
  });
});

test("SdkOciResultSchema rejects payload and tarball digest mismatch", () => {
  assertRejectsSdkOciResult({
    ...validSdkOciResult(),
    tarball_sha256: attestationDigest,
  });
});

test("SdkOciResultSchema rejects invalid npm integrity", () => {
  assertRejectsSdkOciResult({
    ...validSdkOciResult(),
    npm_integrity: "sha512-not-base64",
  });
});

test("SdkOciResultSchema rejects non-npm payload forms", () => {
  assertRejectsSdkOciResult({
    ...validSdkOciResult(),
    payload_form: "python-wheel",
  });
});

test("SdkOciResultSchema rejects attestation digest without ref", () => {
  assertRejectsSdkOciResult({
    ...validSdkOciResult(),
    attestation_digest: attestationDigest,
  });
});

test("SdkOciResultSchema rejects attestation ref without digest", () => {
  assertRejectsSdkOciResult({
    ...validSdkOciResult(),
    attestation_ref: `oci.guardianintelligence.org/guardian/aisucks/sdk/npm@${attestationDigest}`,
  });
});

test("SdkOciResultSchema rejects attestation ref and digest mismatch", () => {
  assertRejectsSdkOciResult({
    ...validSdkOciResult(),
    attestation_digest: attestationDigest,
    attestation_ref: `oci.guardianintelligence.org/guardian/aisucks/sdk/npm@${manifestDigest}`,
  });
});

test("SdkOciResultSchema rejects unrelated wheel digest fields", () => {
  assertRejectsSdkOciResult({
    ...validSdkOciResult(),
    wheel_sha256: payloadDigest,
  });
});

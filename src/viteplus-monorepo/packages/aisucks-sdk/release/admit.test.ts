import assert from "node:assert/strict";
import test from "node:test";
import { Buffer } from "node:buffer";

import { Cause, Effect, Exit, Option } from "effect";

import { admitRelease } from "./admit.js";
import { npmIntegrity, sha256Hex, sha512Hex } from "./digest.js";
import {
  canonicalOciSubjectName,
  defaultChannel,
  defaultOciRef,
  defaultReleasePaths,
  distributable,
  inTotoStatementType,
  intotoPayloadType,
  npmPackagePurl,
  payloadForm,
  sdkPackageName,
  slsaProvenancePredicateType,
  sourceRepo,
  type EvidenceBundle,
  type InTotoStatement,
  type ReleaseCandidate,
  type ReleaseConfig,
  type ReleaseMode,
  type SdkOciResult,
} from "./types.js";

const manifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
const sourceCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";
const version = "0.3.0";
const payload = Buffer.from("guardian aisucks sdk package");

type AdmissionFixture = {
  readonly config: ReleaseConfig;
  readonly candidate: ReleaseCandidate;
  readonly evidence: EvidenceBundle;
};

function validFixture(mode: ReleaseMode = "check"): AdmissionFixture {
  const tarballSha256 = sha256Hex(payload);
  const integrity = npmIntegrity(payload);
  const oci: SdkOciResult = {
    distributable,
    payload_form: payloadForm,
    channel: defaultChannel,
    oci_digest: manifestDigest,
    oci_ref: `/tmp/guardian-sdk-release/oci-layout:${defaultChannel}@${manifestDigest}`,
    payload_sha256: `sha256:${tarballSha256}`,
    tarball_sha256: `sha256:${tarballSha256}`,
    npm_integrity: integrity,
    package: sdkPackageName,
    version,
    source_repo: sourceRepo,
    source_commit: sourceCommit,
    layer_title: "guardian-intelligence-aisucks-0.3.0.tgz",
  };
  const candidate: ReleaseCandidate = {
    target: {
      packageName: sdkPackageName,
      version,
      channel: defaultChannel,
      sourceRepo,
      sourceCommit,
      ociRef: defaultOciRef,
    },
    pack: {
      name: sdkPackageName,
      version,
      filename: "guardian-intelligence-aisucks-0.3.0.tgz",
      integrity,
      size: payload.length,
    },
    oci,
    tarballSha256,
    npmIntegrity: integrity,
    localLayout: "/tmp/guardian-sdk-release/oci-layout",
  };
  const statement = validStatement(candidate);

  return {
    config: {
      mode,
      version,
      channel: defaultChannel,
      ociRef: defaultOciRef,
      publishNpm: mode === "publish",
      publishOci: mode === "publish",
      allowUnsignedDev: mode === "check",
      outputDir: undefined,
      paths: defaultReleasePaths(),
    },
    candidate,
    evidence: evidenceForStatement(statement, {
      signed: mode === "publish",
      tlog: mode === "publish",
    }),
  };
}

function validStatement(candidate: ReleaseCandidate): InTotoStatement {
  return {
    _type: inTotoStatementType,
    subject: [
      {
        name: npmPackagePurl(candidate.target.packageName, candidate.target.version),
        digest: {
          sha256: candidate.tarballSha256,
          sha512: sha512Hex(payload),
        },
      },
      {
        name: canonicalOciSubjectName(candidate.target.ociRef, candidate.oci.oci_digest),
        digest: {
          sha256: candidate.oci.oci_digest.replace(/^sha256:/, ""),
        },
      },
    ],
    predicateType: slsaProvenancePredicateType,
    predicate: {
      buildDefinition: {
        buildType: "https://guardianintelligence.org/release/aisucks-sdk/npm/v1",
      },
    },
  };
}

function evidenceForStatement(
  statement: InTotoStatement,
  options: { readonly signed: boolean; readonly tlog: boolean } = { signed: false, tlog: false },
): EvidenceBundle {
  const statementJson = `${JSON.stringify(statement)}\n`;
  const envelope = {
    payload: Buffer.from(statementJson, "utf8").toString("base64"),
    payloadType: intotoPayloadType,
    signatures: [{ sig: options.signed ? "signed" : "" }],
  };

  return {
    statement,
    statementJson,
    sigstoreBundleJson: JSON.stringify({
      verificationMaterial: {
        tlogEntries: options.tlog ? [{}] : [],
      },
    }),
    intotoJsonl: `${JSON.stringify(envelope)}\n`,
    statementPath: "/tmp/guardian-sdk-release/aisucks-sdk.slsa-provenance.json",
    sigstoreBundlePath: "/tmp/guardian-sdk-release/aisucks-sdk.sigstore.bundle.json",
    intotoBundlePath: "/tmp/guardian-sdk-release/guardian-release.intoto.jsonl",
  };
}

function withEvidence(fixture: AdmissionFixture, evidence: EvidenceBundle): AdmissionFixture {
  return {
    ...fixture,
    evidence,
  };
}

function withCandidate(fixture: AdmissionFixture, candidate: ReleaseCandidate): AdmissionFixture {
  return {
    ...fixture,
    candidate,
  };
}

async function assertAdmitted(fixture: AdmissionFixture): Promise<void> {
  await Effect.runPromise(admitRelease(fixture.config, fixture.candidate, fixture.evidence));
}

async function assertAdmissionRejected(
  fixture: AdmissionFixture,
  expectedReason: string,
): Promise<void> {
  const exit = await Effect.runPromiseExit(
    admitRelease(fixture.config, fixture.candidate, fixture.evidence),
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
  assert.equal(failure.value._tag, "AdmissionRejected");
  if (failure.value._tag !== "AdmissionRejected") {
    return;
  }
  assert.equal(failure.value.reason, expectedReason);
}

void test("admitRelease accepts matching SDK OCI evidence in check mode", async () => {
  await assertAdmitted(validFixture());
});

void test("admitRelease rejects OCI results for a different package", async () => {
  const fixture = validFixture();
  const candidate = {
    ...fixture.candidate,
    oci: {
      ...fixture.candidate.oci,
      package: "@guardian-intelligence/other",
    },
  };

  await assertAdmissionRejected(withCandidate(fixture, candidate), "OCI result package mismatch");
});

void test("admitRelease rejects OCI results for a different source commit", async () => {
  const fixture = validFixture();
  const candidate = {
    ...fixture.candidate,
    oci: {
      ...fixture.candidate.oci,
      source_commit: "cccccccccccccccccccccccccccccccccccccccc",
    },
  };

  await assertAdmissionRejected(
    withCandidate(fixture, candidate),
    "OCI result source commit mismatch",
  );
});

void test("admitRelease rejects OCI results with npm integrity drift", async () => {
  const fixture = validFixture();
  const candidate = {
    ...fixture.candidate,
    oci: {
      ...fixture.candidate.oci,
      npm_integrity: npmIntegrity(Buffer.from("different package bytes")),
    },
  };

  await assertAdmissionRejected(
    withCandidate(fixture, candidate),
    "OCI result npm integrity mismatch",
  );
});

void test("admitRelease rejects an in-toto statement without the OCI subject", async () => {
  const fixture = validFixture();
  const statement = {
    ...fixture.evidence.statement,
    subject: fixture.evidence.statement.subject.filter(
      (subject) => !subject.name.startsWith("oci.guardianintelligence.org/"),
    ),
  };

  await assertAdmissionRejected(
    withEvidence(fixture, evidenceForStatement(statement)),
    "statement missing OCI subject",
  );
});

void test("admitRelease rejects an OCI subject digest mismatch", async () => {
  const fixture = validFixture();
  const statement = {
    ...fixture.evidence.statement,
    subject: fixture.evidence.statement.subject.map((subject) =>
      subject.name.startsWith("oci.guardianintelligence.org/")
        ? {
            ...subject,
            digest: {
              ...subject.digest,
              sha256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
            },
          }
        : subject,
    ),
  };

  await assertAdmissionRejected(
    withEvidence(fixture, evidenceForStatement(statement)),
    "OCI subject sha256 mismatch",
  );
});

void test("admitRelease rejects an in-toto statement without the npm subject", async () => {
  const fixture = validFixture();
  const statement = {
    ...fixture.evidence.statement,
    subject: fixture.evidence.statement.subject.filter(
      (subject) => !subject.name.startsWith("pkg:npm/"),
    ),
  };

  await assertAdmissionRejected(
    withEvidence(fixture, evidenceForStatement(statement)),
    "statement missing npm package subject",
  );
});

void test("admitRelease rejects an npm subject sha512 mismatch", async () => {
  const fixture = validFixture();
  const statement = {
    ...fixture.evidence.statement,
    subject: fixture.evidence.statement.subject.map((subject) =>
      subject.name.startsWith("pkg:npm/")
        ? {
            ...subject,
            digest: {
              ...subject.digest,
              sha512: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
            },
          }
        : subject,
    ),
  };

  await assertAdmissionRejected(
    withEvidence(fixture, evidenceForStatement(statement)),
    "npm subject sha512 mismatch",
  );
});

void test("admitRelease rejects JSONL bundles with more than one attestation", async () => {
  const fixture = validFixture();
  const evidence = {
    ...fixture.evidence,
    intotoJsonl: `${fixture.evidence.intotoJsonl}${fixture.evidence.intotoJsonl}`,
  };

  await assertAdmissionRejected(
    withEvidence(fixture, evidence),
    "JSONL bundle must contain exactly one attestation for this release",
  );
});

void test("admitRelease rejects DSSE envelopes with the wrong payload type", async () => {
  const fixture = validFixture();
  const statementJson = JSON.stringify(fixture.evidence.statement);
  const envelope = {
    payload: Buffer.from(statementJson, "utf8").toString("base64"),
    payloadType: "application/vnd.example.not-intoto+json",
    signatures: [{ sig: "" }],
  };
  const evidence = {
    ...fixture.evidence,
    intotoJsonl: `${JSON.stringify(envelope)}\n`,
  };

  await assertAdmissionRejected(withEvidence(fixture, evidence), "DSSE payload type mismatch");
});

void test("admitRelease rejects DSSE payloads that do not match the admitted statement", async () => {
  const fixture = validFixture();
  const differentStatement = {
    ...fixture.evidence.statement,
    predicate: {
      tampered: true,
    },
  };
  const envelope = {
    payload: Buffer.from(JSON.stringify(differentStatement), "utf8").toString("base64"),
    payloadType: intotoPayloadType,
    signatures: [{ sig: "" }],
  };
  const evidence = {
    ...fixture.evidence,
    intotoJsonl: `${JSON.stringify(envelope)}\n`,
  };

  await assertAdmissionRejected(
    withEvidence(fixture, evidence),
    "DSSE payload does not match in-toto statement",
  );
});

void test("admitRelease rejects unsigned DSSE evidence in publish mode", async () => {
  const fixture = validFixture("publish");
  const evidence = evidenceForStatement(fixture.evidence.statement, {
    signed: false,
    tlog: true,
  });

  await assertAdmissionRejected(
    withEvidence(fixture, evidence),
    "publish mode requires a non-empty DSSE signature",
  );
});

void test("admitRelease rejects publish-mode evidence without a transparency log entry", async () => {
  const fixture = validFixture("publish");
  const evidence = evidenceForStatement(fixture.evidence.statement, {
    signed: true,
    tlog: false,
  });

  await assertAdmissionRejected(
    withEvidence(fixture, evidence),
    "publish mode requires a Sigstore transparency-log entry",
  );
});

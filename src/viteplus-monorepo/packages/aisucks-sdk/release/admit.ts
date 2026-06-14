import { isDeepStrictEqual } from "node:util";
import { Effect } from "effect";

import { parseNpmIntegrity } from "./digest.js";
import { AdmissionRejected, type ReleaseError } from "./errors.js";
import {
  admissionJson,
  decodeJson,
  DsseEnvelopeSchema,
  InTotoStatementSchema,
  SigstoreBundleForAdmissionSchema,
} from "./schemas.js";
import {
  distributable,
  canonicalOciSubjectName,
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
} from "./types.js";

export function admitRelease(
  config: ReleaseConfig,
  candidate: ReleaseCandidate,
  evidence: EvidenceBundle,
): Effect.Effect<void, ReleaseError> {
  return Effect.gen(function* () {
    yield* expect(candidate.target.packageName === sdkPackageName, "unexpected release package", {
      actual: candidate.target.packageName,
      expected: sdkPackageName,
    });
    yield* expect(candidate.pack.name === sdkPackageName, "npm pack metadata package mismatch", {
      actual: candidate.pack.name,
      expected: sdkPackageName,
    });
    yield* expect(candidate.pack.version === config.version, "npm pack metadata version mismatch", {
      actual: candidate.pack.version,
      expected: config.version,
    });
    yield* expect(
      candidate.oci.distributable === distributable,
      "OCI result distributable mismatch",
      {
        actual: candidate.oci.distributable,
        expected: distributable,
      },
    );
    yield* expect(candidate.oci.payload_form === payloadForm, "OCI result payload form mismatch", {
      actual: candidate.oci.payload_form,
      expected: payloadForm,
    });
    yield* expect(candidate.oci.package === sdkPackageName, "OCI result package mismatch", {
      actual: candidate.oci.package,
      expected: sdkPackageName,
    });
    yield* expect(candidate.oci.version === config.version, "OCI result version mismatch", {
      actual: candidate.oci.version,
      expected: config.version,
    });
    yield* expect(candidate.oci.source_repo === sourceRepo, "OCI result source repo mismatch", {
      actual: candidate.oci.source_repo,
      expected: sourceRepo,
    });
    yield* expect(
      candidate.oci.source_commit === candidate.target.sourceCommit,
      "OCI result source commit mismatch",
      {
        actual: candidate.oci.source_commit,
        expected: candidate.target.sourceCommit,
      },
    );
    yield* expect(
      candidate.oci.tarball_sha256 === `sha256:${candidate.tarballSha256}`,
      "OCI result tarball sha256 mismatch",
      {
        actual: candidate.oci.tarball_sha256,
        expected: `sha256:${candidate.tarballSha256}`,
      },
    );
    yield* expect(
      candidate.oci.npm_integrity === candidate.npmIntegrity,
      "OCI result npm integrity mismatch",
      {
        actual: candidate.oci.npm_integrity,
        expected: candidate.npmIntegrity,
      },
    );

    yield* validateStatement(candidate, evidence.statement);
    yield* validateJsonlEnvelope(config, evidence);
  });
}

function validateStatement(
  candidate: ReleaseCandidate,
  statement: InTotoStatement,
): Effect.Effect<void, ReleaseError> {
  return Effect.gen(function* () {
    yield* expect(statement._type === inTotoStatementType, "in-toto statement type mismatch", {
      actual: statement._type,
      expected: inTotoStatementType,
    });
    yield* expect(
      statement.predicateType === slsaProvenancePredicateType,
      "predicate type mismatch",
      {
        actual: statement.predicateType,
        expected: slsaProvenancePredicateType,
      },
    );

    const sha512 = yield* parseNpmIntegrity(candidate.npmIntegrity);
    const npmSubject = statement.subject.find(
      (subject) =>
        subject.name === npmPackagePurl(candidate.target.packageName, candidate.target.version),
    );
    yield* expect(npmSubject !== undefined, "statement missing npm package subject", {
      subject: npmPackagePurl(candidate.target.packageName, candidate.target.version),
    });
    yield* expect(
      npmSubject?.digest.sha256 === candidate.tarballSha256,
      "npm subject sha256 mismatch",
      {
        actual: npmSubject?.digest.sha256,
        expected: candidate.tarballSha256,
      },
    );
    yield* expect(npmSubject?.digest.sha512 === sha512, "npm subject sha512 mismatch", {
      actual: npmSubject?.digest.sha512,
      expected: sha512,
    });

    const ociSubjectName = canonicalOciSubjectName(
      candidate.target.ociRef,
      candidate.oci.oci_digest,
    );
    const ociSubject = statement.subject.find((subject) => subject.name === ociSubjectName);
    yield* expect(ociSubject !== undefined, "statement missing OCI subject", {
      subject: ociSubjectName,
    });
    yield* expect(
      ociSubject?.digest.sha256 === candidate.oci.oci_digest.replace(/^sha256:/, ""),
      "OCI subject sha256 mismatch",
      {
        actual: ociSubject?.digest.sha256,
        expected: candidate.oci.oci_digest.replace(/^sha256:/, ""),
      },
    );
  });
}

function validateJsonlEnvelope(
  config: ReleaseConfig,
  evidence: EvidenceBundle,
): Effect.Effect<void, ReleaseError> {
  return Effect.gen(function* () {
    const lines = evidence.intotoJsonl
      .split("\n")
      .map((line) => line.trim())
      .filter((line) => line !== "");
    yield* expect(
      lines.length === 1,
      "JSONL bundle must contain exactly one attestation for this release",
      {
        lines: lines.length,
      },
    );

    const envelope = yield* decodeJson(DsseEnvelopeSchema, lines[0] ?? "{}", (reason) =>
      admissionJson("DSSE JSONL line does not match schema", { reason }),
    );
    yield* expect(envelope.payloadType === intotoPayloadType, "DSSE payload type mismatch", {
      actual: envelope.payloadType,
      expected: intotoPayloadType,
    });
    const decodedStatement = Buffer.from(envelope.payload, "base64").toString("utf8");
    const statement = yield* decodeJson(InTotoStatementSchema, decodedStatement, (reason) =>
      admissionJson("DSSE payload is not an in-toto statement schema", { reason }),
    );
    yield* expect(
      isDeepStrictEqual(statement, evidence.statement),
      "DSSE payload does not match in-toto statement",
    );

    const nonEmptySignatures = envelope.signatures.filter((signature) => signature.sig !== "");
    if (config.mode === "publish") {
      yield* expect(
        nonEmptySignatures.length > 0,
        "publish mode requires a non-empty DSSE signature",
      );
      const bundle = yield* decodeJson(
        SigstoreBundleForAdmissionSchema,
        evidence.sigstoreBundleJson,
        (reason) => admissionJson("Sigstore bundle does not match admission schema", { reason }),
      );
      yield* expect(
        (bundle.verificationMaterial?.tlogEntries?.length ?? 0) > 0,
        "publish mode requires a Sigstore transparency-log entry",
      );
    }
  });
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

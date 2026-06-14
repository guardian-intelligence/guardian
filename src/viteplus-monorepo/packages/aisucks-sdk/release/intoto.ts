import path from "node:path";
import { Effect } from "effect";
import { attest } from "sigstore";

import { parseNpmIntegrity } from "./digest.js";
import { SigstoreSigningFailed, AdmissionRejected, type ReleaseError } from "./errors.js";
import { FileProvider } from "./providers.js";
import {
  admissionJson,
  decodeUnknown,
  DsseEnvelopeSchema,
  encodeJson,
  InTotoStatementSchema,
  SigstoreBundleSchema,
  SigstoreBundleWithDsseSchema,
  type DsseEnvelope,
} from "./schemas.js";
import {
  distributable,
  canonicalOciSubjectName,
  githubOidcIssuer,
  guardianBuildType,
  inTotoStatementType,
  intotoPayloadType,
  npmPackagePurl,
  payloadForm,
  slsaProvenancePredicateType,
  type EvidenceBundle,
  type InTotoSubject,
  type InTotoStatement,
  type ReleaseCandidate,
  type ReleaseConfig,
} from "./types.js";

export function buildSlsaStatement(
  candidate: ReleaseCandidate,
  invocationId: string,
): Effect.Effect<InTotoStatement, ReleaseError> {
  return Effect.gen(function* () {
    const sha512 = yield* parseNpmIntegrity(candidate.npmIntegrity);
    const subject: readonly InTotoSubject[] = [
      {
        name: npmPackagePurl(candidate.target.packageName, candidate.target.version),
        digest: {
          sha256: candidate.tarballSha256,
          sha512,
        },
      },
      {
        name: canonicalOciSubjectName(candidate.target.ociRef, candidate.oci.oci_digest),
        digest: {
          sha256: candidate.oci.oci_digest.replace(/^sha256:/, ""),
        },
      },
    ];

    return {
      _type: inTotoStatementType,
      subject,
      predicateType: slsaProvenancePredicateType,
      predicate: {
        buildDefinition: {
          buildType: guardianBuildType,
          externalParameters: {
            releaseTarget: {
              distributable,
              payloadForm,
              package: candidate.target.packageName,
              version: candidate.target.version,
              channel: candidate.target.channel,
              ociRef: candidate.target.ociRef,
            },
          },
          internalParameters: {
            bazelTargets: [
              "//src/viteplus-monorepo/packages/aisucks-sdk:npm_package",
              "//src/release/cmd/sdkoci",
            ],
          },
          resolvedDependencies: [
            {
              uri: candidate.target.sourceRepo,
              digest: {
                gitCommit: candidate.target.sourceCommit,
              },
            },
          ],
        },
        runDetails: {
          builder: {
            id: "https://github.com/guardian-intelligence/guardian/.github/workflows/npm-sdk-release.yml",
          },
          metadata: {
            invocationId,
          },
        },
      },
    };
  });
}

export function createEvidenceBundle(
  config: ReleaseConfig,
  candidate: ReleaseCandidate,
  outputDir: string,
): Effect.Effect<EvidenceBundle, ReleaseError, FileProvider> {
  return Effect.gen(function* () {
    const files = yield* FileProvider;
    const invocationId = `${candidate.target.sourceCommit}:${candidate.target.version}:${candidate.target.channel}`;
    const statement = yield* buildSlsaStatement(candidate, invocationId);
    const statementJson = `${yield* encodeJson(
      InTotoStatementSchema,
      statement,
      (reason) => admissionJson("failed to encode SLSA in-toto statement", { reason }),
      { pretty: true },
    )}\n`;
    const statementBytes = Buffer.from(statementJson, "utf8");
    const bundle = yield* signStatement(config, statementBytes);
    const dsse = yield* extractDsseEnvelope(bundle);

    if (dsse.payloadType !== intotoPayloadType) {
      return yield* Effect.fail(
        new AdmissionRejected({
          reason: "Sigstore bundle payload type is not in-toto JSON",
          details: { payloadType: dsse.payloadType },
        }),
      );
    }

    const decodedBundle = yield* decodeUnknown(SigstoreBundleSchema, bundle, (reason) =>
      admissionJson("Sigstore bundle is not an object schema", { reason }),
    );
    const sigstoreBundleJson = `${yield* encodeJson(
      SigstoreBundleSchema,
      decodedBundle,
      (reason) => admissionJson("failed to encode Sigstore bundle", { reason }),
      { pretty: true },
    )}\n`;
    const intotoJsonl = `${yield* encodeJson(DsseEnvelopeSchema, dsse, (reason) =>
      admissionJson("failed to encode DSSE envelope", { reason }),
    )}\n`;
    const statementPath = path.join(outputDir, "aisucks-sdk.slsa-provenance.json");
    const sigstoreBundlePath = path.join(outputDir, "aisucks-sdk.sigstore.bundle.json");
    const intotoBundlePath = path.join(outputDir, "aisucks-sdk.intoto.jsonl");

    yield* files.writeFile(statementPath, statementJson);
    yield* files.writeFile(sigstoreBundlePath, sigstoreBundleJson);
    yield* files.writeFile(intotoBundlePath, intotoJsonl);

    return {
      statement,
      statementJson,
      sigstoreBundleJson,
      intotoJsonl,
      statementPath,
      sigstoreBundlePath,
      intotoBundlePath,
    };
  });
}

function signStatement(
  config: ReleaseConfig,
  statementBytes: Buffer,
): Effect.Effect<unknown, ReleaseError> {
  if (config.mode === "check" && config.allowUnsignedDev) {
    return Effect.succeed(devUnsignedBundle(statementBytes));
  }

  return Effect.tryPromise({
    try: () =>
      attest(statementBytes, intotoPayloadType, {
        tlogUpload: true,
      }),
    catch: (error) =>
      new SigstoreSigningFailed({
        reason: "failed to create Sigstore DSSE attestation",
        details: {
          error: error instanceof Error ? error.message : String(error),
          issuer: githubOidcIssuer,
        },
      }),
  });
}

function extractDsseEnvelope(bundle: unknown): Effect.Effect<DsseEnvelope, ReleaseError> {
  return Effect.gen(function* () {
    const decoded = yield* decodeUnknown(
      SigstoreBundleWithDsseSchema,
      bundle,
      (reason) =>
        new AdmissionRejected({
          reason: "Sigstore bundle schema mismatch",
          details: { reason },
        }),
    );
    if (decoded.dsseEnvelope === undefined) {
      return yield* Effect.fail(
        new AdmissionRejected({ reason: "Sigstore bundle did not contain a DSSE envelope" }),
      );
    }
    return decoded.dsseEnvelope;
  });
}

function devUnsignedBundle(statementBytes: Buffer): unknown {
  return {
    mediaType: "application/vnd.dev.sigstore.bundle.v0.3+json",
    verificationMaterial: {
      publicKey: {
        hint: "guardian-local-check-unsigned",
      },
      tlogEntries: [],
      timestampVerificationData: undefined,
    },
    dsseEnvelope: {
      payload: statementBytes.toString("base64"),
      payloadType: intotoPayloadType,
      signatures: [
        {
          keyid: "guardian-local-check-unsigned",
          sig: "",
        },
      ],
    },
  };
}

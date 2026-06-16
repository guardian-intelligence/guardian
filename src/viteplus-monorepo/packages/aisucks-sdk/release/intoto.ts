import path from "node:path";
import { Effect } from "effect";

import { parseNpmIntegrity } from "./digest.js";
import { type ReleaseError } from "./errors.js";
import { FileProvider } from "./providers.js";
import { admissionJson, encodeJson, InTotoStatementSchema } from "./schemas.js";
import {
  distributable,
  canonicalOciSubjectName,
  guardianBuildType,
  inTotoStatementType,
  npmPackagePurl,
  payloadForm,
  slsaProvenancePredicateType,
  type EvidenceBundle,
  type InTotoSubject,
  type InTotoStatement,
  type ReleaseCandidate,
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
    const statementPath = path.join(outputDir, "aisucks-sdk.slsa-provenance.json");

    yield* files.writeFile(statementPath, statementJson);

    return {
      statement,
      statementJson,
      statementPath,
    };
  });
}

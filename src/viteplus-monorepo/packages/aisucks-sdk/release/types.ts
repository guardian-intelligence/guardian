import { fileURLToPath } from "node:url";
import path from "node:path";

import type {
  InTotoStatementFromSchema,
  NpmPackEntryFromSchema,
  ReleaseResultFromSchema,
  SdkOciResultFromSchema,
} from "./schemas.js";

export const sdkPackageName = "@guardian-intelligence/aisucks";
export const distributable = "aisucks-ts-sdk";
export const payloadForm = "npm";
export const sourceRepo = "https://github.com/guardian-intelligence/guardian";
export const defaultChannel = "edge";
export const defaultOciRef = "oci.guardianintelligence.org/guardian/aisucks/sdk/npm:edge";
export const intotoPayloadType = "application/vnd.in-toto+json";
export const inTotoStatementType = "https://in-toto.io/Statement/v1";
export const slsaProvenancePredicateType = "https://slsa.dev/provenance/v1";
export const guardianBuildType = "https://guardianintelligence.org/release/aisucks-sdk/npm/v1";
export const githubOidcIssuer = "https://token.actions.githubusercontent.com";

export const releaseDir = path.dirname(fileURLToPath(import.meta.url));
export const packageRoot = path.resolve(releaseDir, "..");
export const viteplusRoot = path.resolve(packageRoot, "../..");
export const repoRoot = path.resolve(packageRoot, "../../../..");

export type ReleaseMode = "check" | "publish";

export type ReleasePaths = {
  readonly repoRoot: string;
  readonly packageRoot: string;
  readonly viteplusRoot: string;
  readonly bazelisk: string;
  readonly sdkoci: string;
  readonly cosign: string;
  readonly oras: string;
  readonly npm: string;
  readonly node: string;
  readonly tarball: string;
  readonly packJson: string;
};

export type ReleaseConfig = {
  readonly mode: ReleaseMode;
  readonly version: string;
  readonly channel: string;
  readonly ociRef: string;
  readonly publishNpm: boolean;
  readonly publishOci: boolean;
  readonly createAttestation: boolean;
  readonly signOci: boolean;
  readonly npmProvenance: boolean;
  readonly allowUnsignedDev: boolean;
  readonly outputDir: string | undefined;
  readonly paths: ReleasePaths;
};

export type CommandResult = {
  readonly program: string;
  readonly args: readonly string[];
  readonly cwd: string;
  readonly exitCode: number;
  readonly stdout: string;
  readonly stderr: string;
  readonly durationMs: number;
};

export type ReleaseEvent = {
  readonly stage: string;
  readonly status: "start" | "ok" | "skip" | "fail";
  readonly message: string;
  readonly elapsedMs?: number;
  readonly details?: Readonly<Record<string, unknown>>;
};

export type NpmPackEntry = NpmPackEntryFromSchema;

export type SdkOciResult = SdkOciResultFromSchema;

export type ReleaseCandidate = {
  readonly target: ReleaseTarget;
  readonly pack: NpmPackEntry;
  readonly oci: SdkOciResult;
  readonly tarballSha256: string;
  readonly npmIntegrity: string;
  readonly localLayout: string;
};

export type ReleaseTarget = {
  readonly packageName: string;
  readonly version: string;
  readonly channel: string;
  readonly sourceRepo: string;
  readonly sourceCommit: string;
  readonly ociRef: string;
};

export type SubjectDigest = InTotoStatement["subject"][number]["digest"];

export type InTotoSubject = InTotoStatement["subject"][number];

export type InTotoStatement = InTotoStatementFromSchema;

export type EvidenceBundle = {
  readonly statement: InTotoStatement;
  readonly statementJson: string;
  readonly sigstoreBundleJson: string;
  readonly intotoJsonl: string;
  readonly statementPath: string;
  readonly sigstoreBundlePath: string;
  readonly intotoBundlePath: string;
};

export type ReleaseResult = ReleaseResultFromSchema;

export function defaultReleasePaths(): ReleasePaths {
  return releasePathsForRepoRoot(repoRoot);
}

export function releasePathsForRepoRoot(root: string): ReleasePaths {
  const packageRootForRepo = path.join(root, "src/viteplus-monorepo/packages/aisucks-sdk");
  const viteplusRootForRepo = path.join(root, "src/viteplus-monorepo");
  return {
    repoRoot: root,
    packageRoot: packageRootForRepo,
    viteplusRoot: viteplusRootForRepo,
    bazelisk: "bazelisk",
    sdkoci: path.join(root, "bazel-bin/src/release/cmd/sdkoci/sdkoci_/sdkoci"),
    cosign: "cosign",
    oras: "oras",
    npm: path.join(packageRootForRepo, "node_modules/npm/bin/npm-cli.js"),
    node: path.join(root, "bazel-bin/src/viteplus-monorepo/node"),
    tarball: path.join(
      root,
      "bazel-bin/src/viteplus-monorepo/packages/aisucks-sdk/aisucks-sdk.tgz",
    ),
    packJson: path.join(
      root,
      "bazel-bin/src/viteplus-monorepo/packages/aisucks-sdk/aisucks-sdk.npm-pack.json",
    ),
  };
}

export function npmPackagePurl(packageName: string, version: string): string {
  if (!packageName.startsWith("@")) {
    return `pkg:npm/${encodeURIComponent(packageName)}@${version}`;
  }
  const [scope, name] = packageName.slice(1).split("/");
  return `pkg:npm/%40${encodeURIComponent(scope ?? "")}/${encodeURIComponent(name ?? "")}@${version}`;
}

export function canonicalOciSubjectName(ref: string, digest: string): string {
  if (ref.includes("@")) {
    return ref;
  }
  const lastSlash = ref.lastIndexOf("/");
  const lastColon = ref.lastIndexOf(":");
  if (lastColon > lastSlash) {
    return `${ref.slice(0, lastColon)}@${digest}`;
  }
  return `${ref}@${digest}`;
}

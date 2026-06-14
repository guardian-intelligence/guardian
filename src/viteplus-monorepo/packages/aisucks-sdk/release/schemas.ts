import { Effect } from "effect";
import * as Schema from "effect/Schema";

import {
  AdmissionRejected,
  FileOperationFailed,
  InvalidReleaseTarget,
  VerificationFailed,
  type ReleaseError,
} from "./errors.js";

const stringRecord = Schema.Record({ key: Schema.String, value: Schema.String });
const unknownRecord = Schema.Record({ key: Schema.String, value: Schema.Unknown });
const nonEmptyString = Schema.NonEmptyString;

export const PackageJsonSchema = Schema.Struct({
  name: Schema.optional(Schema.String),
  version: Schema.optional(Schema.String),
});

export const NpmPackEntrySchema = Schema.Struct({
  name: Schema.String,
  version: Schema.String,
  filename: Schema.String,
  integrity: Schema.String,
  shasum: Schema.optional(Schema.String),
  size: Schema.Number,
});

export const NpmPackEntriesSchema = Schema.Array(NpmPackEntrySchema);

export const SdkOciResultSchema = Schema.Struct({
  distributable: nonEmptyString,
  payload_form: nonEmptyString,
  channel: nonEmptyString,
  oci_digest: nonEmptyString,
  oci_ref: nonEmptyString,
  attestation_digest: Schema.optional(nonEmptyString),
  attestation_ref: Schema.optional(nonEmptyString),
  payload_sha256: Schema.optional(nonEmptyString),
  tarball_sha256: nonEmptyString,
  npm_integrity: nonEmptyString,
  package: nonEmptyString,
  version: nonEmptyString,
  source_repo: nonEmptyString,
  source_commit: nonEmptyString,
  layer_title: nonEmptyString,
});

export const InTotoSubjectSchema = Schema.Struct({
  name: Schema.String,
  digest: stringRecord,
});

export const InTotoStatementSchema = Schema.Struct({
  _type: Schema.String,
  subject: Schema.Array(InTotoSubjectSchema),
  predicateType: Schema.String,
  predicate: unknownRecord,
});

export const DsseEnvelopeSchema = Schema.Struct({
  payload: Schema.String,
  payloadType: Schema.String,
  signatures: Schema.Array(
    Schema.Struct({
      sig: Schema.String,
      keyid: Schema.optional(Schema.String),
    }),
  ),
});

export const SigstoreBundleSchema = unknownRecord;

export const SigstoreBundleForAdmissionSchema = Schema.Struct({
  verificationMaterial: Schema.optional(
    Schema.Struct({
      tlogEntries: Schema.optional(Schema.Array(Schema.Unknown)),
    }),
  ),
});

export const SigstoreBundleWithDsseSchema = Schema.Struct({
  dsseEnvelope: Schema.optional(DsseEnvelopeSchema),
  verificationMaterial: Schema.optional(
    Schema.Struct({
      tlogEntries: Schema.optional(Schema.Array(Schema.Unknown)),
    }),
  ),
});

export const ReleaseTargetSchema = Schema.Struct({
  packageName: Schema.String,
  version: Schema.String,
  channel: Schema.String,
  sourceRepo: Schema.String,
  sourceCommit: Schema.String,
  ociRef: Schema.String,
});

export const ReleaseCandidateSchema = Schema.Struct({
  target: ReleaseTargetSchema,
  pack: NpmPackEntrySchema,
  oci: SdkOciResultSchema,
  tarballSha256: Schema.String,
  npmIntegrity: Schema.String,
  localLayout: Schema.String,
});

export const EvidenceBundleSchema = Schema.Struct({
  statement: InTotoStatementSchema,
  statementJson: Schema.String,
  sigstoreBundleJson: Schema.String,
  intotoJsonl: Schema.String,
  statementPath: Schema.String,
  sigstoreBundlePath: Schema.String,
  intotoBundlePath: Schema.String,
});

export const ReleaseEventSchema = Schema.Struct({
  stage: Schema.String,
  status: Schema.Literal("start", "ok", "skip", "fail"),
  message: Schema.String,
  elapsedMs: Schema.optional(Schema.Number),
  details: Schema.optional(unknownRecord),
});

export const ReleaseLogLineSchema = Schema.Struct({
  ts: Schema.String,
  stage: Schema.String,
  status: Schema.Literal("start", "ok", "skip", "fail"),
  message: Schema.String,
  elapsedMs: Schema.optional(Schema.Number),
  details: Schema.optional(unknownRecord),
});

export const ReleaseResultSchema = Schema.Struct({
  target: ReleaseTargetSchema,
  candidate: ReleaseCandidateSchema,
  evidence: EvidenceBundleSchema,
  publishedOci: Schema.optional(SdkOciResultSchema),
  npmStatus: Schema.Literal("not-requested", "published", "already-published"),
  eventLog: Schema.Array(ReleaseEventSchema),
  outputDir: Schema.String,
});

export const ReleaseSummarySchema = Schema.Struct({
  status: Schema.Literal("ok"),
  mode: Schema.Literal("check", "publish"),
  package: Schema.String,
  version: Schema.String,
  channel: Schema.String,
  outputDir: Schema.String,
  ociDigest: Schema.String,
  publishedOciDigest: Schema.optional(Schema.String),
  npmStatus: Schema.Literal("not-requested", "published", "already-published"),
});

export const NpmIntegrityViewSchema = Schema.String;

export const GithubOidcTokenResponseSchema = Schema.Struct({
  value: Schema.String,
});

export const GithubOidcClaimsSchema = Schema.Struct({
  iss: Schema.String,
  sub: Schema.String,
  aud: Schema.Union(Schema.String, Schema.Array(Schema.String)),
  repository: Schema.optional(Schema.String),
  repository_owner: Schema.optional(Schema.String),
  repository_visibility: Schema.optional(Schema.String),
  workflow: Schema.optional(Schema.String),
  workflow_ref: Schema.optional(Schema.String),
  job_workflow_ref: Schema.optional(Schema.String),
  event_name: Schema.optional(Schema.String),
  ref: Schema.optional(Schema.String),
  sha: Schema.optional(Schema.String),
  runner_environment: Schema.optional(Schema.String),
});

export const GithubOidcLogDetailsSchema = Schema.Struct({
  iss: Schema.String,
  sub: Schema.String,
  aud: Schema.Union(Schema.String, Schema.Array(Schema.String)),
  repository: Schema.optional(Schema.String),
  repository_owner: Schema.optional(Schema.String),
  repository_visibility: Schema.optional(Schema.String),
  workflow: Schema.optional(Schema.String),
  workflow_ref: Schema.optional(Schema.String),
  job_workflow_ref: Schema.optional(Schema.String),
  event_name: Schema.optional(Schema.String),
  ref: Schema.optional(Schema.String),
  sha: Schema.optional(Schema.String),
  runner_environment: Schema.optional(Schema.String),
});

export const NpmOidcExchangeResponseSchema = Schema.Struct({
  token: Schema.String,
});

export const NpmOidcExchangeLogDetailsSchema = Schema.Struct({
  packageName: Schema.String,
  registry: Schema.String,
  tokenIssued: Schema.Boolean,
});

export type PackageJson = Schema.Schema.Type<typeof PackageJsonSchema>;
export type NpmPackEntryFromSchema = Schema.Schema.Type<typeof NpmPackEntrySchema>;
export type SdkOciResultFromSchema = Schema.Schema.Type<typeof SdkOciResultSchema>;
export type InTotoStatementFromSchema = Schema.Schema.Type<typeof InTotoStatementSchema>;
export type DsseEnvelope = Schema.Schema.Type<typeof DsseEnvelopeSchema>;
export type ReleaseResultFromSchema = Schema.Schema.Type<typeof ReleaseResultSchema>;
export type GithubOidcClaims = Schema.Schema.Type<typeof GithubOidcClaimsSchema>;

export function decodeUnknown<A, I>(
  schema: Schema.Schema<A, I, never>,
  input: unknown,
  toError: (reason: string) => ReleaseError,
): Effect.Effect<A, ReleaseError> {
  return Schema.decodeUnknown(schema)(input).pipe(
    Effect.mapError((error) => toError(renderParseError(error))),
  );
}

export function decodeJson<A, I>(
  schema: Schema.Schema<A, I, never>,
  input: string,
  toError: (reason: string) => ReleaseError,
): Effect.Effect<A, ReleaseError> {
  return Schema.decode(Schema.parseJson(schema))(input).pipe(
    Effect.mapError((error) => toError(renderParseError(error))),
  );
}

export function decodeJsonSync<A, I>(schema: Schema.Schema<A, I, never>, input: string): A {
  return Schema.decodeSync(Schema.parseJson(schema))(input);
}

export function encodeJson<A, I>(
  schema: Schema.Schema<A, I, never>,
  value: A,
  toError: (reason: string) => ReleaseError,
  options: { readonly pretty?: boolean } = {},
): Effect.Effect<string, ReleaseError> {
  return Schema.encode(
    Schema.parseJson(schema, options.pretty === true ? { space: 2 } : undefined),
  )(value).pipe(Effect.mapError((error) => toError(renderParseError(error))));
}

export function encodeJsonSync<A, I>(
  schema: Schema.Schema<A, I, never>,
  value: A,
  options: { readonly pretty?: boolean } = {},
): string {
  return Schema.encodeSync(
    Schema.parseJson(schema, options.pretty === true ? { space: 2 } : undefined),
  )(value);
}

export function invalidJson(
  reason: string,
  details?: Readonly<Record<string, unknown>>,
): ReleaseError {
  return new InvalidReleaseTarget(errorInput(reason, details));
}

export function admissionJson(
  reason: string,
  details?: Readonly<Record<string, unknown>>,
): ReleaseError {
  return new AdmissionRejected(errorInput(reason, details));
}

export function verificationJson(
  reason: string,
  details?: Readonly<Record<string, unknown>>,
): ReleaseError {
  return new VerificationFailed(errorInput(reason, details));
}

export function fileJson(operation: string, filePath: string): (reason: string) => ReleaseError {
  return (reason) =>
    new FileOperationFailed({
      operation,
      path: filePath,
      reason,
    });
}

function renderParseError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function errorInput(
  reason: string,
  details?: Readonly<Record<string, unknown>>,
): { readonly reason: string; readonly details?: Readonly<Record<string, unknown>> } {
  return details === undefined ? { reason } : { reason, details };
}

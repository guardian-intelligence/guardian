import { Option, Redacted, Schema } from "effect";

export const RepositoryFullName = Schema.String.pipe(
  Schema.pattern(/^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})\/[A-Za-z0-9_.-]{1,100}$/),
  Schema.brand("RepositoryFullName"),
);
export type RepositoryFullName = typeof RepositoryFullName.Type;

export const CommitSha = Schema.String.pipe(
  Schema.pattern(/^[0-9a-f]{40}$/),
  Schema.brand("CommitSha"),
);
export type CommitSha = typeof CommitSha.Type;

export const ExecutionId = Schema.String.pipe(
  Schema.minLength(1),
  Schema.maxLength(256),
  Schema.brand("ExecutionId"),
);
export type ExecutionId = typeof ExecutionId.Type;

export const AttemptId = Schema.String.pipe(
  Schema.minLength(1),
  Schema.maxLength(256),
  Schema.brand("AttemptId"),
);
export type AttemptId = typeof AttemptId.Type;

export const CheckoutPathInput = Schema.String.pipe(
  Schema.minLength(1),
  Schema.maxLength(4096),
  Schema.brand("CheckoutPathInput"),
);
export type CheckoutPathInput = typeof CheckoutPathInput.Type;

export const PackBytes = Schema.Int.pipe(Schema.nonNegative(), Schema.brand("PackBytes"));
export type PackBytes = typeof PackBytes.Type;

export const CanonicalWorkspace = Schema.String.pipe(
  Schema.minLength(1),
  Schema.brand("CanonicalWorkspace"),
);
export type CanonicalWorkspace = typeof CanonicalWorkspace.Type;

export const CanonicalCheckoutTarget = Schema.String.pipe(
  Schema.minLength(1),
  Schema.brand("CanonicalCheckoutTarget"),
);
export type CanonicalCheckoutTarget = typeof CanonicalCheckoutTarget.Type;

export const TempPackPath = Schema.String.pipe(Schema.minLength(1), Schema.brand("TempPackPath"));
export type TempPackPath = typeof TempPackPath.Type;

const RefName = Schema.String.pipe(Schema.minLength(1), Schema.maxLength(1024));

export class BranchRef extends Schema.TaggedClass<BranchRef>()("BranchRef", {
  name: RefName,
  value: RefName,
}) {}

export class TagRef extends Schema.TaggedClass<TagRef>()("TagRef", {
  name: RefName,
  value: RefName,
}) {}

export class PullRequestRef extends Schema.TaggedClass<PullRequestRef>()("PullRequestRef", {
  kind: Schema.Literal("head", "merge"),
  number: Schema.Int.pipe(Schema.positive()),
  value: RefName,
}) {}

export const CheckoutRef = Schema.Union(BranchRef, TagRef, PullRequestRef);
export type CheckoutRef = typeof CheckoutRef.Type;

export const CleanPolicy = Schema.Literal("PreserveBuildState", "ResetTrackedFiles");
export type CleanPolicy = typeof CleanPolicy.Type;

export const CacheHit = Schema.Literal(true, false, "unknown");
export type CacheHit = typeof CacheHit.Type;

export class CheckoutSpec extends Schema.Class<CheckoutSpec>("CheckoutSpec")({
  clean: CleanPolicy,
  expectedCommit: CommitSha,
  fetchDepth: Schema.Literal(1),
  ref: CheckoutRef,
  repository: RepositoryFullName,
  requestedPath: CheckoutPathInput,
}) {}

export class PackMetadata extends Schema.Class<PackMetadata>("PackMetadata")({
  bytes: PackBytes,
  cacheHit: CacheHit,
  sha: CommitSha,
}) {}

export class CheckoutResult extends Schema.Class<CheckoutResult>("CheckoutResult")({
  bundle: PackMetadata,
  commit: CommitSha,
  preexistingHead: Schema.OptionFromSelf(CommitSha),
}) {}

export interface RawActionInputs {
  readonly clean: string;
  readonly fetchDepth: string;
  readonly githubToken: Option.Option<Redacted.Redacted<string>>;
  readonly path: string;
  readonly persistCredentials: string;
  readonly ref: string;
  readonly repository: string;
}

export interface RawRuntimeConfiguration {
  readonly attemptId: string;
  readonly checkoutPath: string;
  readonly checkoutToken: Redacted.Redacted<string>;
  readonly executionId: string;
  readonly hostOrigin: string;
  readonly sha: string;
  readonly workspace: string;
}

export interface ValidatedRuntimeConfiguration {
  readonly attemptId: AttemptId;
  readonly checkoutPath: string;
  readonly checkoutToken: Redacted.Redacted<string>;
  readonly endpoint: URL;
  readonly executionId: ExecutionId;
  readonly githubToken: Option.Option<Redacted.Redacted<string>>;
  readonly workspace: string;
}

export interface TempPack {
  readonly directory: string;
  readonly path: TempPackPath;
}

export interface CheckoutBundleRequest {
  readonly attemptId: AttemptId;
  readonly checkoutToken: Redacted.Redacted<string>;
  readonly destination: TempPackPath;
  readonly endpoint: URL;
  readonly executionId: ExecutionId;
  readonly githubToken: Option.Option<Redacted.Redacted<string>>;
  readonly have: Option.Option<CommitSha>;
  readonly maximumBytes: number;
  readonly spec: CheckoutSpec;
}

export const parseCheckoutRef = (value: string): Option.Option<CheckoutRef> => {
  const branch = /^refs\/heads\/(.+)$/.exec(value);
  if (branch?.[1]) {
    return Option.some(new BranchRef({ name: branch[1], value }));
  }

  const tag = /^refs\/tags\/(.+)$/.exec(value);
  if (tag?.[1]) {
    return Option.some(new TagRef({ name: tag[1], value }));
  }

  const pull = /^refs\/pull\/([1-9][0-9]*)\/(head|merge)$/.exec(value);
  if (pull?.[1] && pull[2]) {
    return Option.some(
      new PullRequestRef({
        kind: pull[2] as "head" | "merge",
        number: Number.parseInt(pull[1], 10),
        value,
      }),
    );
  }

  return Option.none();
};

export const checkoutRefValue = (ref: CheckoutRef): string => ref.value;

import { Context, Effect, Option } from "effect";
import type {
  CanonicalCheckoutTarget,
  CommitSha,
  RepositoryFullName,
  TempPackPath,
} from "../domain.ts";
import type { GitCommandFailed } from "../errors.ts";

export interface GitService {
  readonly checkoutDetached: (
    target: CanonicalCheckoutTarget,
    sha: CommitSha,
  ) => Effect.Effect<void, GitCommandFailed>;
  readonly configureOrigin: (
    target: CanonicalCheckoutTarget,
    repository: RepositoryFullName,
  ) => Effect.Effect<void, GitCommandFailed>;
  readonly configureSafeDirectory: (
    target: CanonicalCheckoutTarget,
  ) => Effect.Effect<void, GitCommandFailed>;
  readonly head: (target: CanonicalCheckoutTarget) => Effect.Effect<CommitSha, GitCommandFailed>;
  readonly importPack: (
    target: CanonicalCheckoutTarget,
    pack: TempPackPath,
  ) => Effect.Effect<void, GitCommandFailed>;
  readonly initialize: (target: CanonicalCheckoutTarget) => Effect.Effect<void, GitCommandFailed>;
  readonly inspectHead: (
    target: CanonicalCheckoutTarget,
  ) => Effect.Effect<Option.Option<CommitSha>, GitCommandFailed>;
  readonly markShallow: (
    target: CanonicalCheckoutTarget,
    sha: CommitSha,
  ) => Effect.Effect<void, GitCommandFailed>;
  readonly resetTrackedFiles: (
    target: CanonicalCheckoutTarget,
  ) => Effect.Effect<void, GitCommandFailed>;
  readonly updateCheckoutRef: (
    target: CanonicalCheckoutTarget,
    sha: CommitSha,
  ) => Effect.Effect<void, GitCommandFailed>;
}

export class Git extends Context.Tag("@guardian/postflight-checkout/Git")<Git, GitService>() {}

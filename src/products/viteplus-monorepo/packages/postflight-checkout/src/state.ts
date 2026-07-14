import { Data, Option } from "effect";
import type {
  CanonicalCheckoutTarget,
  CheckoutResult,
  CheckoutSpec,
  CommitSha,
  PackMetadata,
  RawActionInputs,
  RawRuntimeConfiguration,
  TempPack,
  ValidatedRuntimeConfiguration,
} from "./domain.ts";
import type { CheckoutError } from "./errors.ts";

export type MutationImpact = "TargetMayBePartiallyModified" | "TargetUntouched";

export class Received extends Data.TaggedClass("Received")<{
  readonly inputs: RawActionInputs;
  readonly runtime: RawRuntimeConfiguration;
}> {}

export class Validated extends Data.TaggedClass("Validated")<{
  readonly persistCredentialsRequested: boolean;
  readonly runtime: ValidatedRuntimeConfiguration;
  readonly spec: CheckoutSpec;
}> {}

export class TargetPrepared extends Data.TaggedClass("TargetPrepared")<{
  readonly preexistingHead: Option.Option<CommitSha>;
  readonly runtime: ValidatedRuntimeConfiguration;
  readonly spec: CheckoutSpec;
  readonly target: CanonicalCheckoutTarget;
}> {}

export class PackReady extends Data.TaggedClass("PackReady")<{
  readonly metadata: PackMetadata;
  readonly preexistingHead: Option.Option<CommitSha>;
  readonly spec: CheckoutSpec;
  readonly target: CanonicalCheckoutTarget;
  readonly tempPack: TempPack;
}> {}

export class Materialized extends Data.TaggedClass("Materialized")<{
  readonly actualHead: CommitSha;
  readonly metadata: PackMetadata;
  readonly preexistingHead: Option.Option<CommitSha>;
  readonly spec: CheckoutSpec;
  readonly target: CanonicalCheckoutTarget;
}> {}

export class Verified extends Data.TaggedClass("Verified")<{
  readonly actualHead: CommitSha;
  readonly metadata: PackMetadata;
  readonly preexistingHead: Option.Option<CommitSha>;
  readonly target: CanonicalCheckoutTarget;
}> {}

export class Completed extends Data.TaggedClass("Completed")<{
  readonly result: CheckoutResult;
  readonly target: CanonicalCheckoutTarget;
}> {}

export class Failed extends Data.TaggedClass("Failed")<{
  readonly error: CheckoutError;
  readonly impact: MutationImpact;
  readonly phase: Exclude<CheckoutPhase, "Failed">;
}> {}

export type CheckoutState =
  | Received
  | Validated
  | TargetPrepared
  | PackReady
  | Materialized
  | Verified
  | Completed
  | Failed;

export type CheckoutPhase = CheckoutState["_tag"];

const legalTransitions = {
  Completed: [],
  Failed: [],
  Materialized: ["Verified", "Failed"],
  PackReady: ["Materialized", "Failed"],
  Received: ["Validated", "Failed"],
  TargetPrepared: ["PackReady", "Failed"],
  Validated: ["TargetPrepared", "Failed"],
  Verified: ["Completed", "Failed"],
} as const satisfies Record<CheckoutPhase, ReadonlyArray<CheckoutPhase>>;

export const isLegalTransition = (from: CheckoutPhase, to: CheckoutPhase): boolean =>
  legalTransitions[from].some((candidate) => candidate === to);

export const nextPhases = (phase: CheckoutPhase): ReadonlyArray<CheckoutPhase> =>
  legalTransitions[phase];

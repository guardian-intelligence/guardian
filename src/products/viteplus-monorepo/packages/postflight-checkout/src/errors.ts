import { Schema } from "effect";

const SafeDetail = Schema.String.pipe(Schema.maxLength(4096));
const SafeValue = Schema.String.pipe(Schema.maxLength(1024));

export class InvalidRepository extends Schema.TaggedError<InvalidRepository>()(
  "InvalidRepository",
  { value: SafeValue },
) {
  override get message(): string {
    return `repository must be formatted as owner/name: ${this.value}`;
  }
}

export class UnsupportedRef extends Schema.TaggedError<UnsupportedRef>()("UnsupportedRef", {
  value: SafeValue,
}) {
  override get message(): string {
    return `ref must be a full branch, tag, or pull-request ref: ${this.value}`;
  }
}

export class InvalidCommitSha extends Schema.TaggedError<InvalidCommitSha>()("InvalidCommitSha", {
  value: SafeValue,
}) {
  override get message(): string {
    return `GITHUB_SHA must be a full 40-character commit SHA: ${this.value}`;
  }
}

export class InvalidCheckoutPath extends Schema.TaggedError<InvalidCheckoutPath>()(
  "InvalidCheckoutPath",
  { value: SafeValue },
) {
  override get message(): string {
    return `checkout path is invalid: ${this.value}`;
  }
}

export class UnsupportedFetchDepth extends Schema.TaggedError<UnsupportedFetchDepth>()(
  "UnsupportedFetchDepth",
  { value: SafeValue },
) {
  override get message(): string {
    return `fetch-depth must be 1; received ${this.value}`;
  }
}

export class InvalidBooleanInput extends Schema.TaggedError<InvalidBooleanInput>()(
  "InvalidBooleanInput",
  { name: SafeValue, value: SafeValue },
) {
  override get message(): string {
    return `${this.name} must be a boolean; received ${this.value}`;
  }
}

export class InvalidRuntimeConfiguration extends Schema.TaggedError<InvalidRuntimeConfiguration>()(
  "InvalidRuntimeConfiguration",
  { detail: SafeDetail },
) {
  override get message(): string {
    return `Postflight checkout runtime configuration is invalid: ${this.detail}`;
  }
}

export class WorkspaceEscape extends Schema.TaggedError<WorkspaceEscape>()("WorkspaceEscape", {
  path: SafeValue,
  workspace: SafeValue,
}) {
  override get message(): string {
    return `checkout path ${this.path} must stay inside GITHUB_WORKSPACE ${this.workspace}`;
  }
}

export class WorkspaceFailure extends Schema.TaggedError<WorkspaceFailure>()("WorkspaceFailure", {
  detail: SafeDetail,
  operation: SafeValue,
}) {
  override get message(): string {
    return `workspace ${this.operation} failed: ${this.detail}`;
  }
}

export class HostUnauthorized extends Schema.TaggedError<HostUnauthorized>()("HostUnauthorized", {
  status: Schema.Int,
}) {
  override get message(): string {
    return `checkout control plane rejected runner authentication with HTTP ${this.status}`;
  }
}

export class HostRejected extends Schema.TaggedError<HostRejected>()("HostRejected", {
  status: Schema.Int,
}) {
  override get message(): string {
    return `checkout control plane rejected the request with HTTP ${this.status}`;
  }
}

export class HostUnavailable extends Schema.TaggedError<HostUnavailable>()("HostUnavailable", {
  detail: SafeDetail,
  status: Schema.NullOr(Schema.Int),
}) {
  override get message(): string {
    const status = this.status === null ? "" : ` (HTTP ${this.status})`;
    return `checkout control plane is unavailable${status}: ${this.detail}`;
  }
}

export class PackTooLarge extends Schema.TaggedError<PackTooLarge>()("PackTooLarge", {
  maximumBytes: Schema.Int.pipe(Schema.nonNegative()),
  receivedBytes: Schema.Int.pipe(Schema.nonNegative()),
}) {
  override get message(): string {
    return `checkout pack exceeded ${this.maximumBytes} bytes at ${this.receivedBytes} bytes`;
  }
}

export class PackProtocolMismatch extends Schema.TaggedError<PackProtocolMismatch>()(
  "PackProtocolMismatch",
  { detail: SafeDetail },
) {
  override get message(): string {
    return `checkout control-plane response was invalid: ${this.detail}`;
  }
}

export class GitCommandFailed extends Schema.TaggedError<GitCommandFailed>()("GitCommandFailed", {
  detail: SafeDetail,
  exitCode: Schema.NullOr(Schema.Int),
  operation: SafeValue,
}) {
  override get message(): string {
    const exitCode = this.exitCode === null ? "" : ` (exit ${this.exitCode})`;
    return `git ${this.operation} failed${exitCode}: ${this.detail}`;
  }
}

export class HeadMismatch extends Schema.TaggedError<HeadMismatch>()("HeadMismatch", {
  actual: SafeValue,
  expected: SafeValue,
}) {
  override get message(): string {
    return `checkout produced ${this.actual}; expected ${this.expected}`;
  }
}

export class OutputPublicationFailed extends Schema.TaggedError<OutputPublicationFailed>()(
  "OutputPublicationFailed",
  { detail: SafeDetail },
) {
  override get message(): string {
    return `publishing checkout outputs failed: ${this.detail}`;
  }
}

export type InputError =
  | InvalidRepository
  | UnsupportedRef
  | InvalidCommitSha
  | InvalidCheckoutPath
  | UnsupportedFetchDepth
  | InvalidBooleanInput
  | InvalidRuntimeConfiguration;

export type HostError =
  | HostUnauthorized
  | HostRejected
  | HostUnavailable
  | PackTooLarge
  | PackProtocolMismatch;

export type CheckoutError =
  | InputError
  | WorkspaceEscape
  | WorkspaceFailure
  | HostError
  | GitCommandFailed
  | HeadMismatch
  | OutputPublicationFailed;

export const renderCheckoutError = (error: CheckoutError): string => error.message;

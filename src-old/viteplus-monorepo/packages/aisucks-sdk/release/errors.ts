import { inspect } from "node:util";
import { Data } from "effect";

export type ErrorDetails = Readonly<Record<string, unknown>>;

export class ReleaseUsageError extends Data.TaggedError("ReleaseUsageError")<{
  readonly reason: string;
  readonly details?: ErrorDetails;
}> {}

export class InvalidReleaseTarget extends Data.TaggedError("InvalidReleaseTarget")<{
  readonly reason: string;
  readonly details?: ErrorDetails;
}> {}

export class CommandFailed extends Data.TaggedError("CommandFailed")<{
  readonly program: string;
  readonly args: readonly string[];
  readonly cwd: string;
  readonly exitCode: number | null;
  readonly stdout: string;
  readonly stderr: string;
}> {}

export class CommandTimedOut extends Data.TaggedError("CommandTimedOut")<{
  readonly program: string;
  readonly args: readonly string[];
  readonly cwd: string;
  readonly timeoutMs: number;
  readonly stdout: string;
  readonly stderr: string;
}> {}

export class FileOperationFailed extends Data.TaggedError("FileOperationFailed")<{
  readonly operation: string;
  readonly path: string;
  readonly reason: string;
}> {}

export class DigestMismatch extends Data.TaggedError("DigestMismatch")<{
  readonly artifact: string;
  readonly expected: string;
  readonly actual: string;
}> {}

export class SigstoreSigningFailed extends Data.TaggedError("SigstoreSigningFailed")<{
  readonly reason: string;
  readonly details?: ErrorDetails;
}> {}

export class AdmissionRejected extends Data.TaggedError("AdmissionRejected")<{
  readonly reason: string;
  readonly details?: ErrorDetails;
}> {}

export class PublishConflict extends Data.TaggedError("PublishConflict")<{
  readonly reason: string;
  readonly details?: ErrorDetails;
}> {}

export class VerificationFailed extends Data.TaggedError("VerificationFailed")<{
  readonly reason: string;
  readonly details?: ErrorDetails;
}> {}

export type ReleaseError =
  | ReleaseUsageError
  | InvalidReleaseTarget
  | CommandFailed
  | CommandTimedOut
  | FileOperationFailed
  | DigestMismatch
  | SigstoreSigningFailed
  | AdmissionRejected
  | PublishConflict
  | VerificationFailed;

export function renderReleaseError(error: unknown): string {
  if (typeof error !== "object" || error === null) {
    return String(error);
  }

  if ("_tag" in error && typeof error._tag === "string") {
    const reason = "reason" in error && typeof error.reason === "string" ? `: ${error.reason}` : "";
    return `${error._tag}${reason}\n${inspect(error, { depth: null, colors: false })}`;
  }

  if (error instanceof Error) {
    return `${error.name}: ${error.message}`;
  }

  return inspect(error, { depth: null, colors: false });
}

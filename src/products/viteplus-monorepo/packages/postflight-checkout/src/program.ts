import { Effect, Option, Redacted, Schedule, Schema } from "effect";
import {
  AttemptId,
  CheckoutPathInput,
  CheckoutResult,
  CheckoutSpec,
  CommitSha,
  ExecutionId,
  RepositoryFullName,
  checkoutRefValue,
  parseCheckoutRef,
  type TempPack,
} from "./domain.ts";
import {
  HeadMismatch,
  HostUnavailable,
  InvalidBooleanInput,
  InvalidCheckoutPath,
  InvalidCommitSha,
  InvalidRepository,
  InvalidRuntimeConfiguration,
  UnsupportedFetchDepth,
  UnsupportedRef,
  type CheckoutError,
  type InputError,
} from "./errors.ts";
import { ActionRuntime } from "./services/action-runtime.ts";
import { CheckoutHost } from "./services/checkout-host.ts";
import { Git } from "./services/git.ts";
import { Workspace } from "./services/workspace.ts";
import {
  Completed,
  Failed,
  Materialized,
  PackReady,
  Received,
  TargetPrepared,
  Validated,
  Verified,
  type CheckoutPhase,
  type MutationImpact,
} from "./state.ts";

export const MAXIMUM_PACK_BYTES = 512 * 1024 * 1024;

const errorValue = (value: string): string => value.slice(0, 1024);

const parseBoolean = (
  name: string,
  rawValue: string,
  defaultValue: boolean,
): Effect.Effect<boolean, InvalidBooleanInput> => {
  const value = rawValue.trim().toLowerCase();
  if (value.length === 0) return Effect.succeed(defaultValue);
  if (["1", "true", "yes", "on"].includes(value)) return Effect.succeed(true);
  if (["0", "false", "no", "off"].includes(value)) return Effect.succeed(false);
  return Effect.fail(new InvalidBooleanInput({ name, value: errorValue(rawValue) }));
};

const decodeInput = <A, I>(
  schema: Schema.Schema<A, I>,
  value: unknown,
  error: InputError,
): Effect.Effect<A, InputError> =>
  Schema.decodeUnknown(schema)(value).pipe(Effect.mapError(() => error));

const endpoint = (
  originInput: string,
  pathInput: string,
): Effect.Effect<URL, InvalidRuntimeConfiguration> =>
  Effect.try({
    try: () => {
      const origin = new URL(originInput);
      if (origin.protocol !== "http:" && origin.protocol !== "https:") {
        throw new TypeError("unsupported protocol");
      }
      const path = pathInput.trim().replace(/\/+$/u, "");
      if (!path.startsWith("/") || path.includes("?") || path.includes("#")) {
        throw new TypeError("invalid checkout path");
      }
      return new URL(`${path}/bundle`, origin);
    },
    catch: () =>
      new InvalidRuntimeConfiguration({
        detail: "checkout control-plane origin or path is invalid",
      }),
  });

export const validate = Effect.fn("postflight.checkout.validate")(function* (received: Received) {
  const repository = yield* decodeInput(
    RepositoryFullName,
    received.inputs.repository.trim(),
    new InvalidRepository({
      value: errorValue(received.inputs.repository),
    }),
  );
  const ref = yield* Option.match(parseCheckoutRef(received.inputs.ref.trim()), {
    onNone: () => Effect.fail(new UnsupportedRef({ value: errorValue(received.inputs.ref) })),
    onSome: Effect.succeed,
  });
  const expectedCommit = yield* decodeInput(
    CommitSha,
    received.runtime.sha.trim().toLowerCase(),
    new InvalidCommitSha({ value: errorValue(received.runtime.sha) }),
  );
  const requestedPath = yield* decodeInput(
    CheckoutPathInput,
    received.inputs.path.trim() || ".",
    new InvalidCheckoutPath({ value: errorValue(received.inputs.path) }),
  );
  const clean = yield* parseBoolean("clean", received.inputs.clean, false);
  const persistCredentialsRequested = yield* parseBoolean(
    "persist-credentials",
    received.inputs.persistCredentials,
    false,
  );
  const fetchDepth = received.inputs.fetchDepth.trim() || "1";
  if (fetchDepth !== "1") {
    return yield* Effect.fail(new UnsupportedFetchDepth({ value: errorValue(fetchDepth) }));
  }
  const executionId = yield* decodeInput(
    ExecutionId,
    received.runtime.executionId.trim(),
    new InvalidRuntimeConfiguration({ detail: "execution id is invalid" }),
  );
  const attemptId = yield* decodeInput(
    AttemptId,
    received.runtime.attemptId.trim(),
    new InvalidRuntimeConfiguration({ detail: "attempt id is invalid" }),
  );
  if (received.runtime.workspace.trim().length === 0) {
    return yield* Effect.fail(
      new InvalidRuntimeConfiguration({ detail: "GITHUB_WORKSPACE is empty" }),
    );
  }
  if (Redacted.value(received.runtime.checkoutToken).trim().length === 0) {
    return yield* Effect.fail(
      new InvalidRuntimeConfiguration({
        detail: "POSTFLIGHT_CHECKOUT_TOKEN is empty",
      }),
    );
  }
  const checkoutEndpoint = yield* endpoint(
    received.runtime.hostOrigin,
    received.runtime.checkoutPath,
  );

  return new Validated({
    persistCredentialsRequested,
    runtime: {
      attemptId,
      checkoutPath: received.runtime.checkoutPath,
      checkoutToken: received.runtime.checkoutToken,
      endpoint: checkoutEndpoint,
      executionId,
      githubToken: received.inputs.githubToken,
      workspace: received.runtime.workspace.trim(),
    },
    spec: new CheckoutSpec({
      clean: clean ? "ResetTrackedFiles" : "PreserveBuildState",
      expectedCommit,
      fetchDepth: 1,
      ref,
      repository,
      requestedPath,
    }),
  });
});

export const prepareTarget = Effect.fn("postflight.checkout.prepareTarget")(function* (
  validated: Validated,
) {
  const workspace = yield* Workspace;
  const git = yield* Git;
  const prepared = yield* workspace.prepareTarget(
    validated.runtime.workspace,
    validated.spec.requestedPath,
  );
  const preexistingHead = yield* git.inspectHead(prepared.target);
  return new TargetPrepared({
    preexistingHead,
    runtime: validated.runtime,
    spec: validated.spec,
    target: prepared.target,
  });
});

const retrySchedule = Schedule.exponential("100 millis").pipe(Schedule.jittered);

export const acquirePack = Effect.fn("postflight.checkout.acquirePack")(function* (
  prepared: TargetPrepared,
  tempPack: TempPack,
) {
  const host = yield* CheckoutHost;
  const request = {
    attemptId: prepared.runtime.attemptId,
    checkoutToken: prepared.runtime.checkoutToken,
    destination: tempPack.path,
    endpoint: prepared.runtime.endpoint,
    executionId: prepared.runtime.executionId,
    githubToken: prepared.runtime.githubToken,
    maximumBytes: MAXIMUM_PACK_BYTES,
    spec: prepared.spec,
  } as const;
  const metadata = yield* host.acquirePack(request).pipe(
    Effect.timeoutFail({
      duration: "20 seconds",
      onTimeout: () => new HostUnavailable({ detail: "request timed out", status: null }),
    }),
    Effect.retry({
      schedule: retrySchedule,
      times: 2,
      while: (error) => error._tag === "HostUnavailable",
    }),
    Effect.timeoutFail({
      duration: "45 seconds",
      onTimeout: () =>
        new HostUnavailable({
          detail: "retry deadline expired",
          status: null,
        }),
    }),
  );
  return new PackReady({
    metadata,
    preexistingHead: prepared.preexistingHead,
    spec: prepared.spec,
    target: prepared.target,
    tempPack,
  });
});

export const materialize = Effect.fn("postflight.checkout.materialize")(function* (
  ready: PackReady,
) {
  const git = yield* Git;
  yield* git.initialize(ready.target);
  yield* git.configureOrigin(ready.target, ready.spec.repository);
  yield* git.importPack(ready.target, ready.tempPack.path);
  yield* git.markShallow(ready.target, ready.spec.expectedCommit);
  yield* git.updateCheckoutRef(ready.target, ready.spec.expectedCommit);
  if (ready.spec.clean === "ResetTrackedFiles" && Option.isSome(ready.preexistingHead)) {
    yield* git.resetTrackedFiles(ready.target);
  }
  yield* git.checkoutDetached(ready.target, ready.spec.expectedCommit);
  const actualHead = yield* git.head(ready.target);
  return new Materialized({
    actualHead,
    metadata: ready.metadata,
    preexistingHead: ready.preexistingHead,
    spec: ready.spec,
    target: ready.target,
  });
});

export const verify = Effect.fn("postflight.checkout.verify")((materialized: Materialized) =>
  materialized.actualHead === materialized.spec.expectedCommit
    ? Effect.succeed(
        new Verified({
          actualHead: materialized.actualHead,
          metadata: materialized.metadata,
          preexistingHead: materialized.preexistingHead,
          target: materialized.target,
        }),
      )
    : Effect.fail(
        new HeadMismatch({
          actual: materialized.actualHead,
          expected: materialized.spec.expectedCommit,
        }),
      ),
);

export const complete = Effect.fn("postflight.checkout.complete")(function* (verified: Verified) {
  const git = yield* Git;
  const action = yield* ActionRuntime;
  yield* git.configureSafeDirectory(verified.target);
  const result = new CheckoutResult({
    bundle: verified.metadata,
    commit: verified.actualHead,
    preexistingHead: verified.preexistingHead,
  });
  yield* action.publish(result);
  yield* action.notice(
    `Postflight checked out ${result.commit} (${result.bundle.bytes} bytes, cache ${String(result.bundle.cacheHit)})`,
  );
  return new Completed({ result, target: verified.target });
});

const failed =
  (phase: Exclude<CheckoutPhase, "Failed">, impact: MutationImpact) =>
  (error: CheckoutError): Failed =>
    new Failed({ error, impact, phase });

export const runCheckout = Effect.fn("postflight.checkout.run")(function* (received: Received) {
  const action = yield* ActionRuntime;
  const workspace = yield* Workspace;
  const validated = yield* validate(received).pipe(
    Effect.mapError(failed("Received", "TargetUntouched")),
  );
  if (validated.persistCredentialsRequested) {
    yield* action.notice(
      "persist-credentials is accepted for compatibility; Postflight never persists credentials",
    );
  }
  const prepared = yield* prepareTarget(validated).pipe(
    Effect.mapError(failed("Validated", "TargetUntouched")),
  );
  return yield* Effect.acquireUseRelease(
    workspace.createTempPack.pipe(Effect.mapError(failed("TargetPrepared", "TargetUntouched"))),
    (tempPack) =>
      acquirePack(prepared, tempPack).pipe(
        Effect.mapError(failed("TargetPrepared", "TargetUntouched")),
        Effect.flatMap((ready) =>
          materialize(ready).pipe(
            Effect.mapError(failed("PackReady", "TargetMayBePartiallyModified")),
          ),
        ),
        Effect.flatMap((materialized) =>
          verify(materialized).pipe(
            Effect.mapError(failed("Materialized", "TargetMayBePartiallyModified")),
          ),
        ),
        Effect.flatMap((verified) =>
          complete(verified).pipe(
            Effect.mapError(failed("Verified", "TargetMayBePartiallyModified")),
          ),
        ),
      ),
    (tempPack) => workspace.removeTempPack(tempPack),
  );
});

export const checkoutRequestDescription = (validated: Validated): string =>
  `${validated.spec.repository}@${checkoutRefValue(validated.spec.ref)}:${validated.spec.expectedCommit}`;

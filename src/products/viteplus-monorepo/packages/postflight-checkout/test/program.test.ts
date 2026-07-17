import { expect, it } from "vitest";
import {
  Deferred,
  Effect,
  Fiber,
  Layer,
  Option,
  Redacted,
  Schema,
  TestClock,
  TestContext,
} from "effect";
import {
  AttemptId,
  BranchRef,
  CanonicalCheckoutTarget,
  CanonicalWorkspace,
  CheckoutPathInput,
  CheckoutSpec,
  CommitSha,
  ExecutionId,
  PackBytes,
  PackMetadata,
  RepositoryFullName,
  TempPackPath,
  type CheckoutResult,
  type RawActionInputs,
  type RawRuntimeConfiguration,
  type TempPack,
} from "../src/domain.ts";
import { HostRejected, HostUnavailable } from "../src/errors.ts";
import { acquirePack, runCheckout } from "../src/program.ts";
import { ActionRuntime } from "../src/services/action-runtime.ts";
import { CheckoutHost } from "../src/services/checkout-host.ts";
import { Git } from "../src/services/git.ts";
import { Workspace } from "../src/services/workspace.ts";
import { TargetPrepared, Received } from "../src/state.ts";

const SHA = Schema.decodeUnknownSync(CommitSha)("0123456789abcdef0123456789abcdef01234567");

const input = (): RawActionInputs => ({
  clean: "false",
  fetchDepth: "1",
  githubToken: Option.some(Redacted.make("github-token")),
  path: ".",
  persistCredentials: "false",
  ref: "refs/heads/main",
  repository: "guardian-intelligence/guardian",
});

const runtime = (): RawRuntimeConfiguration => ({
  attemptId: "attempt-1",
  checkoutPath: "/internal/sandbox/v1/github-checkout",
  checkoutToken: Redacted.make("runner-token"),
  executionId: "execution-1",
  hostOrigin: "http://127.0.0.1",
  sha: SHA,
  workspace: "/tmp",
});

const actionHarness = () => {
  const notices: Array<string> = [];
  const outputs: Array<CheckoutResult> = [];
  return {
    layer: Layer.succeed(ActionRuntime, {
      maskSecret: () => Effect.void,
      notice: (message) => Effect.sync(() => notices.push(message)),
      publish: (result) => Effect.sync(() => outputs.push(result)),
      readInputs: Effect.succeed(input()),
      setFailed: (message: string) => Effect.sync(() => notices.push(message)),
    }),
    notices,
    outputs,
  };
};

it("retries only retryable pack acquisition failures", () =>
  Effect.runPromise(
    Effect.gen(function* () {
      let attempts = 0;
      const metadata = new PackMetadata({
        bytes: Schema.decodeUnknownSync(PackBytes)(4),
        cacheHit: false,
        sha: SHA,
      });
      const host = Layer.succeed(CheckoutHost, {
        acquirePack: () =>
          Effect.suspend(() => {
            attempts += 1;
            return attempts < 3
              ? Effect.fail(new HostUnavailable({ detail: "not ready", status: 503 }))
              : Effect.succeed(metadata);
          }),
      });
      const prepared = new TargetPrepared({
        preexistingHead: Option.none(),
        runtime: {
          attemptId: Schema.decodeUnknownSync(AttemptId)("attempt-1"),
          checkoutPath: "/internal/sandbox/v1/github-checkout",
          checkoutToken: Redacted.make("runner-token"),
          endpoint: new URL("http://127.0.0.1/bundle"),
          executionId: Schema.decodeUnknownSync(ExecutionId)("execution-1"),
          githubToken: Option.none(),
          workspace: "/tmp",
        },
        spec: new CheckoutSpec({
          clean: "PreserveBuildState",
          expectedCommit: SHA,
          fetchDepth: 1,
          ref: new BranchRef({ name: "main", value: "refs/heads/main" }),
          repository: Schema.decodeUnknownSync(RepositoryFullName)(
            "guardian-intelligence/guardian",
          ),
          requestedPath: Schema.decodeUnknownSync(CheckoutPathInput)("."),
        }),
        target: Schema.decodeUnknownSync(CanonicalCheckoutTarget)("/tmp"),
      });
      const tempPack: TempPack = {
        directory: "/tmp",
        path: Schema.decodeUnknownSync(TempPackPath)("/tmp/checkout.pack"),
      };
      const fiber = yield* acquirePack(prepared, tempPack).pipe(Effect.provide(host), Effect.fork);
      yield* TestClock.adjust("5 seconds");
      const ready = yield* Fiber.join(fiber);
      expect(ready.metadata).toEqual(metadata);
      expect(attempts).toBe(3);
    }).pipe(Effect.provide(TestContext.TestContext)),
  ));

it("removes the temporary pack when checkout is interrupted", () =>
  Effect.runPromise(
    Effect.gen(function* () {
      const entered = yield* Deferred.make<void>();
      const harness = actionHarness();
      const host = Layer.succeed(CheckoutHost, {
        acquirePack: () => Deferred.succeed(entered, undefined).pipe(Effect.andThen(Effect.never)),
      });
      const tempPack: TempPack = {
        directory: "/tmp/postflight-interruption-test",
        path: Schema.decodeUnknownSync(TempPackPath)(
          "/tmp/postflight-interruption-test/checkout.pack",
        ),
      };
      let removed = false;
      const workspace = Layer.succeed(Workspace, {
        createTempPack: Effect.succeed(tempPack),
        prepareTarget: () =>
          Effect.succeed({
            target: Schema.decodeUnknownSync(CanonicalCheckoutTarget)("/tmp"),
            workspace: Schema.decodeUnknownSync(CanonicalWorkspace)("/tmp"),
          }),
        removeTempPack: () => Effect.sync(() => void (removed = true)),
      });
      const git = Layer.succeed(Git, {
        checkoutDetached: () => Effect.void,
        configureOrigin: () => Effect.void,
        configureSafeDirectory: () => Effect.void,
        head: () => Effect.succeed(SHA),
        importPack: () => Effect.void,
        initialize: () => Effect.void,
        inspectHead: () => Effect.succeed(Option.none()),
        markShallow: () => Effect.void,
        resetTrackedFiles: () => Effect.void,
        updateCheckoutRef: () => Effect.void,
      });
      const fiber = yield* runCheckout(new Received({ inputs: input(), runtime: runtime() })).pipe(
        Effect.provide(Layer.mergeAll(harness.layer, host, workspace, git)),
        Effect.fork,
      );
      yield* Deferred.await(entered);
      yield* Fiber.interrupt(fiber);
      expect(removed).toBe(true);
      expect(harness.outputs).toEqual([]);
    }).pipe(Effect.scoped, Effect.provide(TestContext.TestContext)),
  ));

it("never publishes outputs after a pack failure", () =>
  Effect.runPromise(
    Effect.gen(function* () {
      const harness = actionHarness();
      const host = Layer.succeed(CheckoutHost, {
        acquirePack: () => Effect.fail(new HostRejected({ status: 422 })),
      });
      const tempPack: TempPack = {
        directory: "/tmp/postflight-failure-test",
        path: Schema.decodeUnknownSync(TempPackPath)("/tmp/postflight-failure-test/checkout.pack"),
      };
      const workspace = Layer.succeed(Workspace, {
        createTempPack: Effect.succeed(tempPack),
        prepareTarget: () =>
          Effect.succeed({
            target: Schema.decodeUnknownSync(CanonicalCheckoutTarget)("/tmp"),
            workspace: Schema.decodeUnknownSync(CanonicalWorkspace)("/tmp"),
          }),
        removeTempPack: () => Effect.void,
      });
      const git = Layer.succeed(Git, {
        checkoutDetached: () => Effect.void,
        configureOrigin: () => Effect.void,
        configureSafeDirectory: () => Effect.void,
        head: () => Effect.succeed(SHA),
        importPack: () => Effect.void,
        initialize: () => Effect.void,
        inspectHead: () => Effect.succeed(Option.none()),
        markShallow: () => Effect.void,
        resetTrackedFiles: () => Effect.void,
        updateCheckoutRef: () => Effect.void,
      });
      const result = yield* runCheckout(new Received({ inputs: input(), runtime: runtime() })).pipe(
        Effect.provide(Layer.mergeAll(harness.layer, host, workspace, git)),
        Effect.either,
      );
      expect(result._tag).toBe("Left");
      expect(harness.outputs).toEqual([]);
    }).pipe(Effect.provide(TestContext.TestContext)),
  ));

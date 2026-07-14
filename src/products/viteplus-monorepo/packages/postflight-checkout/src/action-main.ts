import { Effect, Layer } from "effect";
import { loadRuntimeConfiguration } from "./configuration.ts";
import { renderCheckoutError } from "./errors.ts";
import { ActionRuntimeLive } from "./live/action-runtime-node.ts";
import { CheckoutHostLive } from "./live/checkout-host-node.ts";
import { GitLive } from "./live/git-node.ts";
import { WorkspaceLive } from "./live/workspace-node.ts";
import { runCheckout } from "./program.ts";
import { ActionRuntime } from "./services/action-runtime.ts";
import { Failed, Received } from "./state.ts";

const Live = Layer.mergeAll(ActionRuntimeLive, CheckoutHostLive, GitLive, WorkspaceLive);

const action = Effect.gen(function* () {
  const runtime = yield* ActionRuntime;
  const inputs = yield* runtime.readInputs;
  const configuration = yield* loadRuntimeConfiguration.pipe(
    Effect.mapError(
      (error) =>
        new Failed({
          error,
          impact: "TargetUntouched",
          phase: "Received",
        }),
    ),
  );
  yield* runtime.maskSecret(configuration.checkoutToken);
  return yield* runCheckout(new Received({ inputs, runtime: configuration }));
});

const main = action.pipe(
  Effect.catchAll((failure) =>
    ActionRuntime.pipe(
      Effect.flatMap((runtime) =>
        runtime.setFailed(
          `${renderCheckoutError(failure.error)} [phase=${failure.phase}, impact=${failure.impact}]`,
        ),
      ),
    ),
  ),
  Effect.catchAllCause(() =>
    ActionRuntime.pipe(
      Effect.flatMap((runtime) => runtime.setFailed("Postflight checkout failed unexpectedly")),
    ),
  ),
  Effect.provide(Live),
);

void Effect.runPromise(main).catch(() => {
  process.exitCode = 1;
});

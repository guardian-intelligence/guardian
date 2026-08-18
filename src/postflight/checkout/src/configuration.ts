import { Config, Effect, Redacted } from "effect";
import type { RawRuntimeConfiguration } from "./domain.ts";
import { InvalidRuntimeConfiguration } from "./errors.ts";

export const DEFAULT_CHECKOUT_PATH = "/internal/sandbox/v1/github-checkout";

const configuration = Config.all({
  attemptId: Config.string("POSTFLIGHT_ATTEMPT_ID"),
  checkoutPath: Config.string("POSTFLIGHT_CHECKOUT_PATH").pipe(
    Config.withDefault(DEFAULT_CHECKOUT_PATH),
  ),
  checkoutToken: Config.redacted("POSTFLIGHT_CHECKOUT_TOKEN"),
  executionId: Config.string("POSTFLIGHT_EXECUTION_ID"),
  hostOrigin: Config.string("POSTFLIGHT_HOST_SERVICE_HTTP_ORIGIN"),
  sha: Config.string("GITHUB_SHA"),
  workspace: Config.string("GITHUB_WORKSPACE"),
});

export const loadRuntimeConfiguration: Effect.Effect<
  RawRuntimeConfiguration,
  InvalidRuntimeConfiguration
> = configuration.pipe(
  Effect.map((value) => ({
    ...value,
    checkoutToken: Redacted.make(Redacted.value(value.checkoutToken).trim()),
  })),
  Effect.mapError(
    () =>
      new InvalidRuntimeConfiguration({
        detail: "one or more required environment variables are missing",
      }),
  ),
);

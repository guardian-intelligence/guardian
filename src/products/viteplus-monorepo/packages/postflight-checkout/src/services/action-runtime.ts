import { Context, Effect, Redacted } from "effect";
import type { CheckoutResult, RawActionInputs } from "../domain.ts";
import type { OutputPublicationFailed } from "../errors.ts";

export interface ActionRuntimeService {
  readonly maskSecret: (secret: Redacted.Redacted<string>) => Effect.Effect<void>;
  readonly notice: (message: string) => Effect.Effect<void>;
  readonly publish: (result: CheckoutResult) => Effect.Effect<void, OutputPublicationFailed>;
  readonly readInputs: Effect.Effect<RawActionInputs>;
  readonly setFailed: (message: string) => Effect.Effect<void>;
}

export class ActionRuntime extends Context.Tag("@guardian/postflight-checkout/ActionRuntime")<
  ActionRuntime,
  ActionRuntimeService
>() {}

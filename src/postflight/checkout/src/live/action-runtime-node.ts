import * as core from "@actions/core";
import { Effect, Layer, Option, Redacted } from "effect";
import type { CheckoutResult, RawActionInputs } from "../domain.ts";
import { OutputPublicationFailed } from "../errors.ts";
import { ActionRuntime } from "../services/action-runtime.ts";

const input = (name: string): string => core.getInput(name).trim();

const readInputs = Effect.sync((): RawActionInputs => {
  const token = input("token");
  if (token.length > 0) {
    core.setSecret(token);
  }

  return {
    clean: input("clean"),
    fetchDepth: input("fetch-depth"),
    githubToken: token.length === 0 ? Option.none() : Option.some(Redacted.make(token)),
    path: input("path"),
    persistCredentials: input("persist-credentials"),
    ref: input("ref"),
    repository: input("repository"),
  };
});

const publish = (result: CheckoutResult) =>
  Effect.try({
    try: () => {
      core.setOutput(
        "preexisting-head",
        Option.getOrElse(result.preexistingHead, () => ""),
      );
      core.setOutput("commit", result.commit);
      core.setOutput("bundle-cache-hit", result.bundle.cacheHit);
    },
    catch: () => new OutputPublicationFailed({ detail: "GitHub output command failed" }),
  });

export const ActionRuntimeLive = Layer.succeed(ActionRuntime, {
  maskSecret: (secret) => Effect.sync(() => core.setSecret(Redacted.value(secret))),
  notice: (message) => Effect.sync(() => core.notice(message)),
  publish,
  readInputs,
  setFailed: (message) => Effect.sync(() => core.setFailed(message)),
});

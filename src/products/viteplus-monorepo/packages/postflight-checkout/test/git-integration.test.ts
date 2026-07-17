import { mkdir, readdir, symlink, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, it } from "vitest";
import { Effect, Either, Layer, Option, Redacted, Schema } from "effect";
import {
  CheckoutPathInput,
  PackBytes,
  PackMetadata,
  type CheckoutResult,
  type CommitSha,
  type RawActionInputs,
  type RawRuntimeConfiguration,
} from "../src/domain.ts";
import { HostUnavailable } from "../src/errors.ts";
import { makeGitLive } from "../src/live/git-node.ts";
import { makeWorkspaceLive } from "../src/live/workspace-node.ts";
import { runCheckout } from "../src/program.ts";
import { ActionRuntime } from "../src/services/action-runtime.ts";
import { CheckoutHost } from "../src/services/checkout-host.ts";
import { Workspace } from "../src/services/workspace.ts";
import { Received } from "../src/state.ts";
import { makeGitFixture, readText } from "./control-plane-fixtures.ts";

const actionInputs = (clean: boolean): RawActionInputs => ({
  clean: String(clean),
  fetchDepth: "1",
  githubToken: Option.some(Redacted.make("github-token")),
  path: "checkout",
  persistCredentials: "false",
  ref: "refs/heads/main",
  repository: "guardian-intelligence/guardian",
});

const runtime = (workspace: string, sha: CommitSha): RawRuntimeConfiguration => ({
  attemptId: "attempt-1",
  checkoutPath: "/internal/sandbox/v1/github-checkout",
  checkoutToken: Redacted.make("runner-token"),
  executionId: "execution-1",
  hostOrigin: "http://127.0.0.1",
  sha,
  workspace,
});

const actionHarness = () => {
  const notices: Array<string> = [];
  const outputs: Array<CheckoutResult> = [];
  return {
    layer: Layer.succeed(ActionRuntime, {
      maskSecret: () => Effect.void,
      notice: (message) => Effect.sync(() => notices.push(message)),
      publish: (result) => Effect.sync(() => outputs.push(result)),
      readInputs: Effect.die("not used in integration tests"),
      setFailed: (message) => Effect.sync(() => notices.push(message)),
    }),
    notices,
    outputs,
  };
};

it("materializes exact commits while preserving durable build state", async () => {
  const fixture = await makeGitFixture();
  try {
    const first = await fixture.commit("first\n");
    const packs = new Map<CommitSha, Uint8Array>([[first.sha, first.pack]]);
    const harness = actionHarness();
    const host = Layer.succeed(CheckoutHost, {
      acquirePack: (request) =>
        Effect.tryPromise({
          try: async () => {
            const pack = packs.get(request.spec.expectedCommit);
            if (!pack) throw new Error("fixture pack missing");
            await writeFile(request.destination, pack, { mode: 0o600 });
            return new PackMetadata({
              bytes: Schema.decodeUnknownSync(PackBytes)(pack.byteLength),
              cacheHit: false,
              sha: request.spec.expectedCommit,
            });
          },
          catch: () => new HostUnavailable({ detail: "fixture pack missing", status: null }),
        }),
    });
    const services = Layer.mergeAll(
      harness.layer,
      host,
      makeGitLive({ environment: { HOME: fixture.home } }),
      makeWorkspaceLive({ tempDirectory: fixture.tempPacks }),
    );

    const firstCompleted = await Effect.runPromise(
      runCheckout(
        new Received({
          inputs: actionInputs(false),
          runtime: runtime(fixture.workspace, first.sha),
        }),
      ).pipe(Effect.provide(services)),
    );
    const target = join(fixture.workspace, "checkout");
    expect(firstCompleted.result.commit).toBe(first.sha);
    expect(await readText(join(target, "tracked.txt"))).toBe("first\n");
    expect(harness.outputs).toHaveLength(1);
    expect(await readdir(fixture.tempPacks)).toEqual([]);

    await writeFile(join(target, "build.cache"), "durable artifact\n");
    await writeFile(join(target, "tracked.txt"), "locally dirty\n");
    const second = await fixture.commit("second\n");
    packs.set(second.sha, second.pack);

    const secondCompleted = await Effect.runPromise(
      runCheckout(
        new Received({
          inputs: actionInputs(false),
          runtime: runtime(fixture.workspace, second.sha),
        }),
      ).pipe(Effect.provide(services)),
    );
    expect(secondCompleted.result.commit).toBe(second.sha);
    expect(secondCompleted.result.preexistingHead).toEqual(Option.some(first.sha));
    expect(await readText(join(target, "tracked.txt"))).toBe("second\n");
    expect(await readText(join(target, "build.cache"))).toBe("durable artifact\n");
    expect(await readdir(fixture.tempPacks)).toEqual([]);
    expect(harness.outputs).toHaveLength(2);
  } finally {
    await fixture.cleanup();
  }
});

it("rejects a symlink escape after canonicalizing the target", async () => {
  const fixture = await makeGitFixture();
  try {
    const outside = join(fixture.root, "outside");
    await mkdir(outside);
    await symlink(outside, join(fixture.workspace, "escape"), "dir");
    const result = await Effect.runPromise(
      Effect.gen(function* () {
        const workspace = yield* Workspace;
        return yield* workspace.prepareTarget(
          fixture.workspace,
          Schema.decodeUnknownSync(CheckoutPathInput)("escape/checkout"),
        );
      }).pipe(
        Effect.provide(makeWorkspaceLive({ tempDirectory: fixture.tempPacks })),
        Effect.either,
      ),
    );
    expect(Either.isLeft(result)).toBe(true);
    if (Either.isLeft(result)) expect(result.left._tag).toBe("WorkspaceEscape");
  } finally {
    await fixture.cleanup();
  }
});

it("removes the pack and withholds outputs after Git rejects it", async () => {
  const fixture = await makeGitFixture();
  try {
    const commit = await fixture.commit("first\n");
    const harness = actionHarness();
    const host = Layer.succeed(CheckoutHost, {
      acquirePack: (request) =>
        Effect.tryPromise({
          try: async () => {
            const pack = Uint8Array.from([1, 2, 3, 4]);
            await writeFile(request.destination, pack, { mode: 0o600 });
            return new PackMetadata({
              bytes: Schema.decodeUnknownSync(PackBytes)(pack.byteLength),
              cacheHit: false,
              sha: request.spec.expectedCommit,
            });
          },
          catch: () => new HostUnavailable({ detail: "fixture write failed", status: null }),
        }),
    });
    const result = await Effect.runPromise(
      runCheckout(
        new Received({
          inputs: actionInputs(false),
          runtime: runtime(fixture.workspace, commit.sha),
        }),
      ).pipe(
        Effect.provide(
          Layer.mergeAll(
            harness.layer,
            host,
            makeGitLive({ environment: { HOME: fixture.home } }),
            makeWorkspaceLive({ tempDirectory: fixture.tempPacks }),
          ),
        ),
        Effect.either,
      ),
    );
    expect(Either.isLeft(result)).toBe(true);
    if (Either.isLeft(result)) {
      expect(result.left.error._tag).toBe("GitCommandFailed");
      expect(result.left.impact).toBe("TargetMayBePartiallyModified");
    }
    expect(harness.outputs).toEqual([]);
    expect(await readdir(fixture.tempPacks)).toEqual([]);
  } finally {
    await fixture.cleanup();
  }
});

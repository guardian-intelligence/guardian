import { createReadStream } from "node:fs";
import { readFile, writeFile } from "node:fs/promises";
import { EOL } from "node:os";
import { spawn } from "node:child_process";
import { isAbsolute, resolve } from "node:path";
import { Effect, Layer, Option, Schema } from "effect";
import {
  CommitSha,
  type CanonicalCheckoutTarget,
  type RepositoryFullName,
  type TempPackPath,
} from "../domain.ts";
import { GitCommandFailed } from "../errors.ts";
import { Git } from "../services/git.ts";

const MAX_OUTPUT_BYTES = 64 * 1024;

interface CommandResult {
  readonly exitCode: number;
  readonly stderr: string;
  readonly stdout: string;
}

export interface GitLiveOptions {
  readonly environment?: NodeJS.ProcessEnv;
}

const boundedAppend = (current: string, chunk: Buffer): string =>
  (current + chunk.toString("utf8")).slice(0, MAX_OUTPUT_BYTES);

const commandFailure = (operation: string, result: CommandResult): GitCommandFailed =>
  new GitCommandFailed({
    detail: result.stderr.trim().slice(0, 4096) || "command failed",
    exitCode: result.exitCode,
    operation,
  });

const makeRunner =
  (options: GitLiveOptions) =>
  (
    operation: string,
    cwd: CanonicalCheckoutTarget,
    args: ReadonlyArray<string>,
    inputPath?: TempPackPath,
  ): Effect.Effect<CommandResult, GitCommandFailed> =>
    Effect.async<CommandResult, GitCommandFailed>((resume) => {
      const child = spawn("git", ["-c", `safe.directory=${cwd}`, ...args], {
        cwd,
        env: options.environment ? { ...process.env, ...options.environment } : process.env,
        stdio: [inputPath ? "pipe" : "ignore", "pipe", "pipe"],
      });
      let completed = false;
      let stderr = "";
      let stdout = "";
      const input = inputPath ? createReadStream(inputPath) : undefined;

      const finish = (effect: Effect.Effect<CommandResult, GitCommandFailed>) => {
        if (completed) return;
        completed = true;
        resume(effect);
      };

      child.stdout?.on("data", (chunk: Buffer) => {
        stdout = boundedAppend(stdout, chunk);
      });
      child.stderr?.on("data", (chunk: Buffer) => {
        stderr = boundedAppend(stderr, chunk);
      });
      child.on("error", () => {
        finish(
          Effect.fail(
            new GitCommandFailed({
              detail: "could not start git",
              exitCode: null,
              operation,
            }),
          ),
        );
      });
      child.on("close", (exitCode) => {
        finish(
          Effect.succeed({
            exitCode: exitCode ?? -1,
            stderr,
            stdout,
          }),
        );
      });

      if (input && child.stdin) {
        input.on("error", () => {
          child.kill("SIGTERM");
          finish(
            Effect.fail(
              new GitCommandFailed({
                detail: "could not read checkout pack",
                exitCode: null,
                operation,
              }),
            ),
          );
        });
        input.pipe(child.stdin);
      }

      return Effect.sync(() => {
        input?.destroy();
        if (!completed) child.kill("SIGTERM");
      });
    });

const safeFsDetail = (error: unknown): string =>
  (error instanceof Error ? error.message : String(error)).slice(0, 4096);

export const makeGitLive = (options: GitLiveOptions = {}) => {
  const run = makeRunner(options);

  const checked = (
    operation: string,
    cwd: CanonicalCheckoutTarget,
    args: ReadonlyArray<string>,
    inputPath?: TempPackPath,
  ) =>
    run(operation, cwd, args, inputPath).pipe(
      Effect.flatMap((result) =>
        result.exitCode === 0
          ? Effect.succeed(result)
          : Effect.fail(commandFailure(operation, result)),
      ),
    );

  const inspectHead = (target: CanonicalCheckoutTarget) =>
    run("inspect HEAD", target, ["rev-parse", "--verify", "HEAD"]).pipe(
      Effect.flatMap((result) => {
        if (result.exitCode !== 0) return Effect.succeed(Option.none());
        try {
          return Effect.succeed(
            Option.some(Schema.decodeUnknownSync(CommitSha)(result.stdout.trim().toLowerCase())),
          );
        } catch {
          return Effect.fail(
            new GitCommandFailed({
              detail: "HEAD was not a full commit SHA",
              exitCode: result.exitCode,
              operation: "inspect HEAD",
            }),
          );
        }
      }),
    );

  const configureOrigin = (target: CanonicalCheckoutTarget, repository: RepositoryFullName) => {
    const url = `https://github.com/${repository}.git`;
    return run("set origin", target, ["remote", "set-url", "origin", url]).pipe(
      Effect.flatMap((result) =>
        result.exitCode === 0
          ? Effect.void
          : checked("add origin", target, ["remote", "add", "origin", url]).pipe(Effect.asVoid),
      ),
    );
  };

  const markShallow = (target: CanonicalCheckoutTarget, sha: typeof CommitSha.Type) =>
    checked("locate shallow file", target, ["rev-parse", "--git-path", "shallow"]).pipe(
      Effect.flatMap((result) => {
        const shallowOutput = result.stdout.trim();
        const shallowPath = isAbsolute(shallowOutput)
          ? shallowOutput
          : resolve(target, shallowOutput);
        return Effect.tryPromise({
          try: async () => {
            let existing = "";
            try {
              existing = await readFile(shallowPath, "utf8");
            } catch (error) {
              if (
                typeof error !== "object" ||
                error === null ||
                !("code" in error) ||
                error.code !== "ENOENT"
              ) {
                throw error;
              }
            }
            const commits = new Set(
              existing
                .split(/\r?\n/u)
                .map((value) => value.trim())
                .filter((value) => value.length > 0),
            );
            commits.add(sha);
            await writeFile(shallowPath, `${Array.from(commits).sort().join(EOL)}${EOL}`);
          },
          catch: (error) =>
            new GitCommandFailed({
              detail: safeFsDetail(error),
              exitCode: null,
              operation: "mark shallow commit",
            }),
        });
      }),
    );

  const head = (target: CanonicalCheckoutTarget) =>
    checked("read HEAD", target, ["rev-parse", "HEAD"]).pipe(
      Effect.flatMap((result) => {
        try {
          return Effect.succeed(
            Schema.decodeUnknownSync(CommitSha)(result.stdout.trim().toLowerCase()),
          );
        } catch {
          return Effect.fail(
            new GitCommandFailed({
              detail: "HEAD was not a full commit SHA",
              exitCode: result.exitCode,
              operation: "read HEAD",
            }),
          );
        }
      }),
    );

  const configureSafeDirectory = (target: CanonicalCheckoutTarget) =>
    run("list safe directories", target, [
      "config",
      "--global",
      "--get-all",
      "safe.directory",
    ]).pipe(
      Effect.flatMap((result) => {
        const directories = result.stdout
          .split(/\r?\n/u)
          .map((value) => value.trim())
          .filter((value) => value.length > 0);
        return directories.includes(target)
          ? Effect.void
          : checked("configure safe directory", target, [
              "config",
              "--global",
              "--add",
              "safe.directory",
              target,
            ]).pipe(Effect.asVoid);
      }),
    );

  return Layer.succeed(Git, {
    checkoutDetached: (target, sha) =>
      checked("checkout", target, ["checkout", "--force", "--detach", sha]).pipe(Effect.asVoid),
    configureOrigin,
    configureSafeDirectory,
    head,
    importPack: (target, pack) =>
      checked("index pack", target, ["index-pack", "--fix-thin", "--stdin"], pack).pipe(
        Effect.asVoid,
      ),
    initialize: (target) => checked("initialize", target, ["init"]).pipe(Effect.asVoid),
    inspectHead,
    markShallow,
    resetTrackedFiles: (target) =>
      checked("reset tracked files", target, ["reset", "--hard", "HEAD"]).pipe(Effect.asVoid),
    updateCheckoutRef: (target, sha) =>
      checked("update checkout ref", target, ["update-ref", "refs/postflight/checkout", sha]).pipe(
        Effect.asVoid,
      ),
  });
};

export const GitLive = makeGitLive();

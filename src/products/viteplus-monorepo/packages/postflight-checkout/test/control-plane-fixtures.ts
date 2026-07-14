import { spawn } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Schema } from "effect";
import { CommitSha, type CommitSha as CommitShaType } from "../src/domain.ts";

interface CommandResult {
  readonly stderr: Buffer;
  readonly stdout: Buffer;
}

const command = (
  executable: string,
  args: ReadonlyArray<string>,
  cwd: string,
  environment: NodeJS.ProcessEnv,
  input?: string,
): Promise<CommandResult> =>
  new Promise((resolve, reject) => {
    const child = spawn(executable, args, {
      cwd,
      env: { ...process.env, ...environment },
      stdio: [input === undefined ? "ignore" : "pipe", "pipe", "pipe"],
    });
    const stderr: Array<Buffer> = [];
    const stdout: Array<Buffer> = [];
    child.stderr?.on("data", (chunk: Buffer) => stderr.push(chunk));
    child.stdout?.on("data", (chunk: Buffer) => stdout.push(chunk));
    child.on("error", reject);
    child.on("close", (exitCode) => {
      const result = {
        stderr: Buffer.concat(stderr),
        stdout: Buffer.concat(stdout),
      };
      if (exitCode === 0) {
        resolve(result);
      } else {
        reject(
          new Error(
            `${executable} ${args.join(" ")} exited ${String(exitCode)}: ${result.stderr.toString("utf8")}`,
          ),
        );
      }
    });
    if (input !== undefined && child.stdin) child.stdin.end(input);
  });

export interface CommitFixture {
  readonly pack: Uint8Array;
  readonly sha: CommitShaType;
}

export interface GitFixture {
  readonly cleanup: () => Promise<void>;
  readonly commit: (contents: string) => Promise<CommitFixture>;
  readonly home: string;
  readonly root: string;
  readonly tempPacks: string;
  readonly workspace: string;
}

export const makeGitFixture = async (): Promise<GitFixture> => {
  const root = await mkdtemp(join(tmpdir(), "postflight-checkout-test-"));
  const home = join(root, "home");
  const repository = join(root, "source");
  const tempPacks = join(root, "packs");
  const workspace = join(root, "workspace");
  const environment = { HOME: home };

  await Promise.all(
    [home, repository, tempPacks, workspace].map((path) => mkdir(path, { recursive: true })),
  );
  await command("git", ["init"], repository, environment);
  await command(
    "git",
    ["config", "user.email", "test@postflight.invalid"],
    repository,
    environment,
  );
  await command("git", ["config", "user.name", "Postflight Test"], repository, environment);

  return {
    cleanup: () => rm(root, { force: true, recursive: true }),
    commit: async (contents) => {
      await writeFile(join(repository, "tracked.txt"), contents);
      await command("git", ["add", "tracked.txt"], repository, environment);
      await command(
        "git",
        ["commit", "--quiet", "--message", `fixture ${contents}`],
        repository,
        environment,
      );
      const shaOutput = await command("git", ["rev-parse", "HEAD"], repository, environment);
      const sha = Schema.decodeUnknownSync(CommitSha)(shaOutput.stdout.toString("utf8").trim());
      const packOutput = await command(
        "git",
        ["pack-objects", "--stdout", "--revs"],
        repository,
        environment,
        `${sha}\n`,
      );
      return { pack: packOutput.stdout, sha };
    },
    home,
    root,
    tempPacks,
    workspace,
  };
};

export const readText = (path: string): Promise<string> => readFile(path, "utf8");

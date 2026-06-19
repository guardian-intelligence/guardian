import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { Context, Effect, Layer } from "effect";

import {
  CommandFailed,
  CommandTimedOut,
  FileOperationFailed,
  type ReleaseError,
} from "./errors.js";
import { encodeJsonSync, ReleaseLogLineSchema } from "./schemas.js";
import type { CommandResult, ReleaseEvent } from "./types.js";

export type CommandInput = {
  readonly program: string;
  readonly args: readonly string[];
  readonly redactedArgs?: readonly string[];
  readonly cwd: string;
  readonly env?: Readonly<Record<string, string>>;
  readonly stdin?: string | Buffer;
  readonly timeoutMs: number;
};

export type ProcessService = {
  readonly run: (input: CommandInput) => Effect.Effect<CommandResult, ReleaseError>;
};

export class ProcessProvider extends Context.Tag("ProcessProvider")<
  ProcessProvider,
  ProcessService
>() {}

export type FileService = {
  readonly readFile: (filePath: string) => Effect.Effect<Buffer, ReleaseError>;
  readonly writeFile: (
    filePath: string,
    data: string | Buffer,
  ) => Effect.Effect<void, ReleaseError>;
  readonly mkdir: (dirPath: string) => Effect.Effect<void, ReleaseError>;
  readonly mkdtemp: (prefix: string) => Effect.Effect<string, ReleaseError>;
  readonly rm: (targetPath: string) => Effect.Effect<void, ReleaseError>;
};

export class FileProvider extends Context.Tag("FileProvider")<FileProvider, FileService>() {}

export type LoggerService = {
  readonly log: (event: ReleaseEvent) => Effect.Effect<void>;
  readonly events: () => readonly ReleaseEvent[];
};

export class LoggerProvider extends Context.Tag("LoggerProvider")<
  LoggerProvider,
  LoggerService
>() {}

export const NodeProcessLayer = Layer.succeed(ProcessProvider, {
  run: (input) =>
    Effect.tryPromise({
      try: () => runCommand(input),
      catch: (error) =>
        error instanceof CommandFailed || error instanceof CommandTimedOut
          ? error
          : new CommandFailed({
              program: input.program,
              args: input.args,
              cwd: input.cwd,
              exitCode: null,
              stdout: "",
              stderr: error instanceof Error ? error.message : String(error),
            }),
    }),
});

export const NodeFileLayer = Layer.succeed(FileProvider, {
  readFile: (filePath) =>
    Effect.tryPromise({
      try: () => fs.readFile(filePath),
      catch: (error) =>
        new FileOperationFailed({
          operation: "readFile",
          path: filePath,
          reason: error instanceof Error ? error.message : String(error),
        }),
    }),
  writeFile: (filePath, data) =>
    Effect.tryPromise({
      try: async () => {
        await fs.mkdir(path.dirname(filePath), { recursive: true });
        await fs.writeFile(filePath, data);
      },
      catch: (error) =>
        new FileOperationFailed({
          operation: "writeFile",
          path: filePath,
          reason: error instanceof Error ? error.message : String(error),
        }),
    }),
  mkdir: (dirPath) =>
    Effect.tryPromise({
      try: async () => {
        await fs.mkdir(dirPath, { recursive: true });
      },
      catch: (error) =>
        new FileOperationFailed({
          operation: "mkdir",
          path: dirPath,
          reason: error instanceof Error ? error.message : String(error),
        }),
    }),
  mkdtemp: (prefix) =>
    Effect.tryPromise({
      try: () => fs.mkdtemp(path.join(os.tmpdir(), prefix)),
      catch: (error) =>
        new FileOperationFailed({
          operation: "mkdtemp",
          path: os.tmpdir(),
          reason: error instanceof Error ? error.message : String(error),
        }),
    }),
  rm: (targetPath) =>
    Effect.tryPromise({
      try: () => fs.rm(targetPath, { force: true, recursive: true }),
      catch: (error) =>
        new FileOperationFailed({
          operation: "rm",
          path: targetPath,
          reason: error instanceof Error ? error.message : String(error),
        }),
    }),
});

export function makeMemoryLoggerLayer(): Layer.Layer<LoggerProvider> {
  const events: ReleaseEvent[] = [];

  return Layer.succeed(LoggerProvider, {
    log: (event) =>
      Effect.sync(() => {
        events.push(event);
        process.stderr.write(
          `${encodeJsonSync(ReleaseLogLineSchema, { ts: new Date().toISOString(), ...event })}\n`,
        );
      }),
    events: () => events,
  });
}

function runCommand(input: CommandInput): Promise<CommandResult> {
  return new Promise((resolve, reject) => {
    const started = Date.now();
    const child = spawn(input.program, [...input.args], {
      cwd: input.cwd,
      env: { ...process.env, ...input.env },
      stdio: [input.stdin === undefined ? "ignore" : "pipe", "pipe", "pipe"],
    });
    const loggedArgs = input.redactedArgs ?? input.args;
    const childStdout = child.stdout;
    const childStderr = child.stderr;
    const childStdin = child.stdin;

    const stdout: Buffer[] = [];
    const stderr: Buffer[] = [];
    let timedOut = false;

    const timer = setTimeout(() => {
      timedOut = true;
      child.kill("SIGTERM");
      setTimeout(() => child.kill("SIGKILL"), 2_000).unref();
    }, input.timeoutMs);

    if (childStdout === null || childStderr === null) {
      clearTimeout(timer);
      child.kill("SIGTERM");
      reject(
        new CommandFailed({
          program: input.program,
          args: loggedArgs,
          cwd: input.cwd,
          exitCode: null,
          stdout: "",
          stderr: "failed to open subprocess stdio pipes",
        }),
      );
      return;
    }

    childStdout.on("data", (chunk: Buffer) => stdout.push(chunk));
    childStderr.on("data", (chunk: Buffer) => stderr.push(chunk));
    if (input.stdin !== undefined) {
      if (childStdin === null) {
        clearTimeout(timer);
        child.kill("SIGTERM");
        reject(
          new CommandFailed({
            program: input.program,
            args: loggedArgs,
            cwd: input.cwd,
            exitCode: null,
            stdout: "",
            stderr: "failed to open subprocess stdin pipe",
          }),
        );
        return;
      }
      childStdin.end(input.stdin);
    }

    child.on("error", (error) => {
      clearTimeout(timer);
      reject(
        new CommandFailed({
          program: input.program,
          args: loggedArgs,
          cwd: input.cwd,
          exitCode: null,
          stdout: Buffer.concat(stdout).toString("utf8"),
          stderr: error.message,
        }),
      );
    });

    child.on("close", (code) => {
      clearTimeout(timer);
      const result = {
        program: input.program,
        args: loggedArgs,
        cwd: input.cwd,
        exitCode: code ?? 0,
        stdout: Buffer.concat(stdout).toString("utf8"),
        stderr: Buffer.concat(stderr).toString("utf8"),
        durationMs: Date.now() - started,
      };

      if (timedOut) {
        reject(
          new CommandTimedOut({
            program: input.program,
            args: loggedArgs,
            cwd: input.cwd,
            timeoutMs: input.timeoutMs,
            stdout: result.stdout,
            stderr: result.stderr,
          }),
        );
        return;
      }

      if (code !== 0) {
        reject(new CommandFailed(result));
        return;
      }

      resolve(result);
    });
  });
}

import { lstat, mkdir, mkdtemp, open, realpath, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, isAbsolute, join, relative, resolve, sep } from "node:path";
import { Effect, Layer, Schema } from "effect";
import {
  CanonicalCheckoutTarget,
  CanonicalWorkspace,
  TempPackPath,
  type CheckoutPathInput,
} from "../domain.ts";
import { WorkspaceEscape, WorkspaceFailure } from "../errors.ts";
import { Workspace } from "../services/workspace.ts";

const safeDetail = (error: unknown): string => {
  const detail = error instanceof Error ? error.message : String(error);
  return detail.slice(0, 4096);
};

const isMissing = (error: unknown): boolean =>
  typeof error === "object" && error !== null && "code" in error && error.code === "ENOENT";

const isContained = (workspace: string, candidate: string): boolean => {
  const pathFromWorkspace = relative(workspace, candidate);
  return (
    pathFromWorkspace === "" ||
    (!pathFromWorkspace.startsWith(`..${sep}`) &&
      pathFromWorkspace !== ".." &&
      !isAbsolute(pathFromWorkspace))
  );
};

const nearestExistingAncestor = async (
  target: string,
): Promise<{ readonly ancestor: string; readonly suffix: ReadonlyArray<string> }> => {
  const suffix: Array<string> = [];
  let candidate = target;

  for (;;) {
    try {
      await lstat(candidate);
      return { ancestor: candidate, suffix };
    } catch (error) {
      if (!isMissing(error)) throw error;
      const parent = dirname(candidate);
      if (parent === candidate) throw error;
      suffix.unshift(candidate.slice(parent.length + 1));
      candidate = parent;
    }
  }
};

const prepareTarget = (workspaceInput: string, requestedPath: CheckoutPathInput) =>
  Effect.tryPromise({
    try: async () => {
      const workspace = await realpath(workspaceInput);
      if (isAbsolute(requestedPath)) {
        throw new WorkspaceEscape({
          path: requestedPath,
          workspace,
        });
      }

      const lexicalTarget = resolve(workspace, requestedPath);
      if (!isContained(workspace, lexicalTarget)) {
        throw new WorkspaceEscape({
          path: requestedPath,
          workspace,
        });
      }

      const { ancestor, suffix } = await nearestExistingAncestor(lexicalTarget);
      const canonicalAncestor = await realpath(ancestor);
      const candidate = resolve(canonicalAncestor, ...suffix);
      if (!isContained(workspace, candidate)) {
        throw new WorkspaceEscape({
          path: requestedPath,
          workspace,
        });
      }

      await mkdir(candidate, { recursive: true });
      const target = await realpath(candidate);
      if (!isContained(workspace, target)) {
        throw new WorkspaceEscape({
          path: requestedPath,
          workspace,
        });
      }

      return {
        target: Schema.decodeUnknownSync(CanonicalCheckoutTarget)(target),
        workspace: Schema.decodeUnknownSync(CanonicalWorkspace)(workspace),
      };
    },
    catch: (error) =>
      error instanceof WorkspaceEscape
        ? error
        : new WorkspaceFailure({
            detail: safeDetail(error),
            operation: "prepare target",
          }),
  });

export interface WorkspaceLiveOptions {
  readonly tempDirectory?: string;
}

export const makeWorkspaceLive = (options: WorkspaceLiveOptions = {}) =>
  Layer.succeed(Workspace, {
    createTempPack: Effect.tryPromise({
      try: async () => {
        const directory = await mkdtemp(
          join(options.tempDirectory ?? tmpdir(), "postflight-checkout-"),
        );
        const path = join(directory, "checkout.pack");
        const file = await open(path, "w", 0o600);
        await file.close();
        return {
          directory,
          path: Schema.decodeUnknownSync(TempPackPath)(path),
        };
      },
      catch: (error) =>
        new WorkspaceFailure({
          detail: safeDetail(error),
          operation: "create temporary pack",
        }),
    }),
    prepareTarget,
    removeTempPack: (tempPack) =>
      Effect.tryPromise(() => rm(tempPack.directory, { force: true, recursive: true })).pipe(
        Effect.catchAll((error) =>
          Effect.logWarning("failed to remove temporary checkout pack").pipe(
            Effect.annotateLogs("detail", safeDetail(error)),
          ),
        ),
      ),
  });

export const WorkspaceLive = makeWorkspaceLive();

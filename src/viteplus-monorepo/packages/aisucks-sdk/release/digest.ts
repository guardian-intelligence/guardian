import { createHash } from "node:crypto";
import { Effect } from "effect";

import { DigestMismatch, InvalidReleaseTarget, type ReleaseError } from "./errors.js";
import { FileProvider } from "./providers.js";
import { decodeJson, invalidJson, NpmPackEntriesSchema } from "./schemas.js";
import type { NpmPackEntry } from "./types.js";

export function sha256Hex(data: Buffer): string {
  return createHash("sha256").update(data).digest("hex");
}

export function sha512Hex(data: Buffer): string {
  return createHash("sha512").update(data).digest("hex");
}

export function npmIntegrity(data: Buffer): string {
  return `sha512-${createHash("sha512").update(data).digest("base64")}`;
}

export function parseNpmIntegrity(integrity: string): Effect.Effect<string, ReleaseError> {
  return Effect.try({
    try: () => {
      if (!integrity.startsWith("sha512-")) {
        throw new Error("not sha512 SRI");
      }
      return Buffer.from(integrity.slice("sha512-".length), "base64").toString("hex");
    },
    catch: () =>
      new InvalidReleaseTarget({
        reason: "npm integrity is not valid sha512 SRI",
        details: { integrity },
      }),
  });
}

export function readPackEntry(
  filePath: string,
): Effect.Effect<NpmPackEntry, ReleaseError, FileProvider> {
  return Effect.gen(function* () {
    const files = yield* FileProvider;
    const raw = yield* files.readFile(filePath);
    const parsed = yield* decodeJson(NpmPackEntriesSchema, raw.toString("utf8"), (reason) =>
      invalidJson("npm pack metadata is not valid JSON for the expected schema", {
        filePath,
        reason,
      }),
    );

    if (parsed.length !== 1) {
      return yield* Effect.fail(
        new InvalidReleaseTarget({
          reason: "npm pack metadata must contain exactly one package entry",
          details: { filePath },
        }),
      );
    }

    const entry = parsed[0];
    if (entry === undefined) {
      return yield* Effect.fail(
        new InvalidReleaseTarget({
          reason: "npm pack metadata entry is unexpectedly missing",
          details: { filePath },
        }),
      );
    }
    return entry;
  });
}

export function verifyTarballBytes(
  tarballPath: string,
  pack: NpmPackEntry,
): Effect.Effect<
  { readonly data: Buffer; readonly sha256: string; readonly integrity: string },
  ReleaseError,
  FileProvider
> {
  return Effect.gen(function* () {
    const files = yield* FileProvider;
    const data = yield* files.readFile(tarballPath);
    const actualIntegrity = npmIntegrity(data);
    const actualSha256 = sha256Hex(data);

    if (data.length !== pack.size) {
      return yield* Effect.fail(
        new DigestMismatch({
          artifact: tarballPath,
          expected: `${pack.size} bytes`,
          actual: `${data.length} bytes`,
        }),
      );
    }
    if (actualIntegrity !== pack.integrity) {
      return yield* Effect.fail(
        new DigestMismatch({
          artifact: tarballPath,
          expected: pack.integrity,
          actual: actualIntegrity,
        }),
      );
    }

    return { data, sha256: actualSha256, integrity: actualIntegrity };
  });
}

import { open } from "node:fs/promises";
import { Effect, Layer, Option, Redacted, Schema } from "effect";
import {
  CommitSha,
  PackBytes,
  PackMetadata,
  checkoutRefValue,
  type CacheHit,
  type CheckoutBundleRequest,
} from "../domain.ts";
import {
  HostRejected,
  HostUnauthorized,
  HostUnavailable,
  PackProtocolMismatch,
  PackTooLarge,
  type HostError,
} from "../errors.ts";
import { CheckoutHost } from "../services/checkout-host.ts";

const CACHE_HIT_HEADER = "x-postflight-checkout-bundle-cache-hit";
const SHA_HEADER = "x-postflight-checkout-sha";
const SIZE_HEADER = "x-postflight-checkout-size-bytes";

const hostErrorTags = new Set([
  "HostRejected",
  "HostUnauthorized",
  "HostUnavailable",
  "PackProtocolMismatch",
  "PackTooLarge",
]);

const isHostError = (error: unknown): error is HostError =>
  typeof error === "object" &&
  error !== null &&
  "_tag" in error &&
  typeof error._tag === "string" &&
  hostErrorTags.has(error._tag);

const parseSize = (value: string | null, name: string): number => {
  if (value === null || !/^(0|[1-9][0-9]*)$/.test(value)) {
    throw new PackProtocolMismatch({ detail: `${name} is missing or invalid` });
  }
  const parsed = Number(value);
  if (!Number.isSafeInteger(parsed)) {
    throw new PackProtocolMismatch({ detail: `${name} exceeds a safe integer` });
  }
  return parsed;
};

const parseCacheHit = (value: string | null): CacheHit => {
  if (value === null) return "unknown";
  if (value === "true") return true;
  if (value === "false") return false;
  throw new PackProtocolMismatch({
    detail: `${CACHE_HIT_HEADER} must be true or false`,
  });
};

const classifyStatus = (status: number, maximumBytes: number): HostError => {
  if (status === 401 || status === 403) {
    return new HostUnauthorized({ status });
  }
  if (status === 413) {
    return new PackTooLarge({
      maximumBytes,
      receivedBytes: maximumBytes + 1,
    });
  }
  if (status === 429 || status >= 500) {
    return new HostUnavailable({
      detail: "request failed",
      status,
    });
  }
  return new HostRejected({ status });
};

const acquirePack = (fetchImplementation: typeof fetch, request: CheckoutBundleRequest) =>
  Effect.tryPromise({
    try: async (signal) => {
      const response = await fetchImplementation(request.endpoint, {
        body: JSON.stringify({
          ...(Option.isSome(request.githubToken)
            ? { github_token: Redacted.value(request.githubToken.value) }
            : {}),
          ...(Option.isSome(request.have) ? { have: request.have.value } : {}),
          ref: checkoutRefValue(request.spec.ref),
          repository: request.spec.repository,
          sha: request.spec.expectedCommit,
        }),
        headers: {
          Authorization: `Bearer ${Redacted.value(request.checkoutToken)}`,
          "Content-Type": "application/json",
          "X-Postflight-Attempt-Id": request.attemptId,
          "X-Postflight-Execution-Id": request.executionId,
        },
        method: "POST",
        // A redirect would re-send the request body — including the GitHub
        // token — to whatever host the origin names. The control-plane
        // endpoint never redirects, so treat any redirect as hostile.
        redirect: "error",
        signal,
      });

      if (response.status !== 200) {
        throw classifyStatus(response.status, request.maximumBytes);
      }

      const contentType = response.headers.get("content-type");
      if (
        contentType === null ||
        !contentType.toLowerCase().startsWith("application/x-git-packed-objects")
      ) {
        throw new PackProtocolMismatch({
          detail: "content-type was not application/x-git-packed-objects",
        });
      }

      const responseSha = response.headers.get(SHA_HEADER);
      let sha: typeof CommitSha.Type;
      try {
        sha = Schema.decodeUnknownSync(CommitSha)(responseSha);
      } catch {
        throw new PackProtocolMismatch({
          detail: `${SHA_HEADER} is missing or invalid`,
        });
      }
      if (sha !== request.spec.expectedCommit) {
        throw new PackProtocolMismatch({
          detail: `${SHA_HEADER} did not match the requested commit`,
        });
      }

      const declaredSize = parseSize(response.headers.get(SIZE_HEADER), SIZE_HEADER);
      const contentLengthValue = response.headers.get("content-length");
      const contentLength =
        contentLengthValue === null ? null : parseSize(contentLengthValue, "content-length");

      for (const size of [declaredSize, contentLength]) {
        if (size !== null && size > request.maximumBytes) {
          throw new PackTooLarge({
            maximumBytes: request.maximumBytes,
            receivedBytes: size,
          });
        }
      }
      if (contentLength !== null && contentLength !== declaredSize) {
        throw new PackProtocolMismatch({
          detail: "content-length did not match the declared pack size",
        });
      }
      if (response.body === null) {
        throw new PackProtocolMismatch({ detail: "response body was empty" });
      }

      const file = await open(request.destination, "w", 0o600);
      const reader = response.body.getReader();
      let receivedBytes = 0;
      try {
        for (;;) {
          const next = await reader.read();
          if (next.done) break;
          receivedBytes += next.value.byteLength;
          if (receivedBytes > request.maximumBytes) {
            throw new PackTooLarge({
              maximumBytes: request.maximumBytes,
              receivedBytes,
            });
          }
          await file.write(next.value);
        }
      } finally {
        await file.close();
        reader.releaseLock();
      }

      if (receivedBytes !== declaredSize) {
        throw new PackProtocolMismatch({
          detail: "streamed bytes did not match the declared pack size",
        });
      }

      return new PackMetadata({
        bytes: Schema.decodeUnknownSync(PackBytes)(receivedBytes),
        cacheHit: parseCacheHit(response.headers.get(CACHE_HIT_HEADER)),
        sha,
      });
    },
    catch: (error) =>
      isHostError(error)
        ? error
        : new HostUnavailable({
            detail: "request or response stream failed",
            status: null,
          }),
  });

export const makeCheckoutHostLive = (fetchImplementation: typeof fetch = globalThis.fetch) =>
  Layer.succeed(CheckoutHost, {
    acquirePack: (request) => acquirePack(fetchImplementation, request),
  });

export const CheckoutHostLive = makeCheckoutHostLive();

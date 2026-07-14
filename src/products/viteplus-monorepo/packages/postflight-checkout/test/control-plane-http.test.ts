import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "@effect/vitest";
import { Effect, Either, Option, Redacted, Schema } from "effect";
import {
  AttemptId,
  BranchRef,
  CheckoutPathInput,
  CheckoutSpec,
  CommitSha,
  ExecutionId,
  RepositoryFullName,
  TempPackPath,
  type CheckoutBundleRequest,
} from "../src/domain.ts";
import { makeCheckoutHostLive } from "../src/live/checkout-host-node.ts";
import { CheckoutHost } from "../src/services/checkout-host.ts";

const SHA = Schema.decodeUnknownSync(CommitSha)("0123456789abcdef0123456789abcdef01234567");

const responseHeaders = (size: number, overrides: Record<string, string> = {}): Headers =>
  new Headers({
    "Content-Length": String(size),
    "Content-Type": "application/x-git-packed-objects",
    "X-Postflight-Checkout-Bundle-Cache-Hit": "true",
    "X-Postflight-Checkout-Sha": SHA,
    "X-Postflight-Checkout-Size-Bytes": String(size),
    ...overrides,
  });

const withDestination = async <A>(
  use: (request: CheckoutBundleRequest, destination: string) => Promise<A>,
): Promise<A> => {
  const directory = await mkdtemp(join(tmpdir(), "postflight-http-test-"));
  const destination = join(directory, "checkout.pack");
  await writeFile(destination, "");
  const request: CheckoutBundleRequest = {
    attemptId: Schema.decodeUnknownSync(AttemptId)("attempt-1"),
    checkoutToken: Redacted.make("runner-secret"),
    destination: Schema.decodeUnknownSync(TempPackPath)(destination),
    endpoint: new URL("http://127.0.0.1/internal/sandbox/v1/github-checkout/bundle"),
    executionId: Schema.decodeUnknownSync(ExecutionId)("execution-1"),
    githubToken: Option.some(Redacted.make("github-secret")),
    maximumBytes: 1024,
    spec: new CheckoutSpec({
      clean: "PreserveBuildState",
      expectedCommit: SHA,
      fetchDepth: 1,
      ref: new BranchRef({ name: "main", value: "refs/heads/main" }),
      repository: Schema.decodeUnknownSync(RepositoryFullName)("guardian-intelligence/guardian"),
      requestedPath: Schema.decodeUnknownSync(CheckoutPathInput)("."),
    }),
  };
  try {
    return await use(request, destination);
  } finally {
    await rm(directory, { force: true, recursive: true });
  }
};

const execute = (fetchImplementation: typeof fetch, request: CheckoutBundleRequest) =>
  Effect.gen(function* () {
    const host = yield* CheckoutHost;
    return yield* host.acquirePack(request);
  }).pipe(Effect.provide(makeCheckoutHostLive(fetchImplementation)));

describe("checkout control-plane HTTP client", () => {
  it("sends the authenticated contract and streams the pack", async () => {
    await withDestination(async (request, destination) => {
      const bytes = Uint8Array.from([1, 2, 3, 4, 5]);
      let capturedInput: string | URL | Request | undefined;
      let capturedInit: RequestInit | undefined;
      const mockFetch: typeof fetch = async (input, init) => {
        capturedInput = input;
        capturedInit = init;
        return new Response(bytes, { headers: responseHeaders(bytes.length) });
      };

      const metadata = await Effect.runPromise(execute(mockFetch, request));
      expect(metadata.bytes).toBe(bytes.length);
      expect(metadata.cacheHit).toBe(true);
      expect(metadata.sha).toBe(SHA);
      expect(capturedInput).toBeInstanceOf(URL);
      if (!(capturedInput instanceof URL) || capturedInit === undefined) {
        throw new TypeError("fetch invocation was not captured");
      }
      expect(capturedInput.toString()).toBe(request.endpoint.toString());
      expect(capturedInit.method).toBe("POST");
      const headers = new Headers(capturedInit.headers);
      expect(headers.get("Authorization")).toBe("Bearer runner-secret");
      expect(headers.get("X-Postflight-Execution-Id")).toBe("execution-1");
      if (typeof capturedInit.body !== "string") {
        throw new TypeError("checkout request body was not JSON text");
      }
      expect(JSON.parse(capturedInit.body)).toEqual({
        github_token: "github-secret",
        ref: "refs/heads/main",
        repository: "guardian-intelligence/guardian",
        sha: SHA,
      });
      expect(await readFile(destination)).toEqual(Buffer.from(bytes));
    });
  });

  it("honors the contract across a real Node HTTP boundary", async () => {
    const bytes = Uint8Array.from([1, 2, 3, 4, 5]);
    let receivedAuthorization: string | undefined;
    let receivedBody: unknown;
    let receivedPath: string | undefined;
    const server = createServer((incoming, outgoing) => {
      const chunks: Array<Buffer> = [];
      incoming.on("data", (chunk: Buffer) => chunks.push(chunk));
      incoming.on("end", () => {
        receivedAuthorization = incoming.headers.authorization;
        receivedBody = JSON.parse(Buffer.concat(chunks).toString("utf8"));
        receivedPath = incoming.url;
        outgoing.writeHead(200, Object.fromEntries(responseHeaders(bytes.length)));
        outgoing.end(bytes);
      });
    });
    await new Promise<void>((resolve, reject) => {
      server.once("error", reject);
      server.listen(0, "127.0.0.1", resolve);
    });

    try {
      const address = server.address() as AddressInfo;
      await withDestination(async (baseRequest, destination) => {
        const request = {
          ...baseRequest,
          endpoint: new URL(
            `http://127.0.0.1:${address.port}/internal/sandbox/v1/github-checkout/bundle`,
          ),
        };
        const metadata = await Effect.runPromise(execute(globalThis.fetch, request));
        expect(metadata.bytes).toBe(bytes.length);
        expect(receivedAuthorization).toBe("Bearer runner-secret");
        expect(receivedPath).toBe("/internal/sandbox/v1/github-checkout/bundle");
        expect(receivedBody).toEqual({
          github_token: "github-secret",
          ref: "refs/heads/main",
          repository: "guardian-intelligence/guardian",
          sha: SHA,
        });
        expect(await readFile(destination)).toEqual(Buffer.from(bytes));
      });
    } finally {
      server.closeAllConnections();
      await new Promise<void>((resolve, reject) => {
        server.close((error) => (error === undefined ? resolve() : reject(error)));
      });
    }
  });

  it("accepts an omitted cache-hit header as unknown", async () => {
    await withDestination(async (request) => {
      const bytes = Uint8Array.from([1, 2]);
      const headers = responseHeaders(bytes.length);
      headers.delete("X-Postflight-Checkout-Bundle-Cache-Hit");
      const metadata = await Effect.runPromise(
        execute(async () => new Response(bytes, { headers }), request),
      );
      expect(metadata.cacheHit).toBe("unknown");
    });
  });

  it("rejects a mismatched response SHA without exposing credentials", async () => {
    await withDestination(async (request) => {
      const bytes = Uint8Array.from([1]);
      const result = await Effect.runPromise(
        execute(
          async () =>
            new Response(bytes, {
              headers: responseHeaders(bytes.length, {
                "X-Postflight-Checkout-Sha": "ffffffffffffffffffffffffffffffffffffffff",
              }),
            }),
          request,
        ).pipe(Effect.either),
      );
      expect(Either.isLeft(result)).toBe(true);
      if (Either.isLeft(result)) {
        expect(result.left._tag).toBe("PackProtocolMismatch");
        expect(result.left.message).not.toContain("runner-secret");
        expect(result.left.message).not.toContain("github-secret");
      }
    });
  });

  it("enforces the streamed-byte maximum", async () => {
    await withDestination(async (baseRequest) => {
      const request = { ...baseRequest, maximumBytes: 2 };
      const body = new ReadableStream<Uint8Array>({
        start(controller) {
          controller.enqueue(Uint8Array.from([1, 2]));
          controller.enqueue(Uint8Array.from([3]));
          controller.close();
        },
      });
      const result = await Effect.runPromise(
        execute(
          async () =>
            new Response(body, {
              headers: responseHeaders(2, { "Content-Length": "2" }),
            }),
          request,
        ).pipe(Effect.either),
      );
      expect(Either.isLeft(result)).toBe(true);
      if (Either.isLeft(result)) expect(result.left._tag).toBe("PackTooLarge");
    });
  });

  it("maps interrupted streams to a retryable host failure", async () => {
    await withDestination(async (request) => {
      const body = new ReadableStream<Uint8Array>({
        start(controller) {
          controller.enqueue(Uint8Array.from([1]));
          controller.error(new Error("stream contained github-secret"));
        },
      });
      const result = await Effect.runPromise(
        execute(
          async () =>
            new Response(body, {
              headers: responseHeaders(2),
            }),
          request,
        ).pipe(Effect.either),
      );
      expect(Either.isLeft(result)).toBe(true);
      if (Either.isLeft(result)) {
        expect(result.left._tag).toBe("HostUnavailable");
        expect(result.left.message).not.toContain("github-secret");
      }
    });
  });

  it.each([
    [401, "HostUnauthorized"],
    [403, "HostUnauthorized"],
    [404, "HostRejected"],
    [413, "PackTooLarge"],
    [422, "HostRejected"],
    [429, "HostUnavailable"],
    [500, "HostUnavailable"],
  ] as const)("maps HTTP %i to %s", async (status, tag) => {
    await withDestination(async (request) => {
      const result = await Effect.runPromise(
        execute(async () => new Response(null, { status }), request).pipe(Effect.either),
      );
      expect(Either.isLeft(result)).toBe(true);
      if (Either.isLeft(result)) expect(result.left._tag).toBe(tag);
    });
  });
});

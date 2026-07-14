import { describe, expect, it } from "@effect/vitest";
import { Option, Schema } from "effect";
import { CommitSha, RepositoryFullName, parseCheckoutRef } from "../src/domain.ts";
import { isLegalTransition, nextPhases, type CheckoutPhase } from "../src/state.ts";

describe("checkout domain", () => {
  it("accepts exact commit SHAs and owner/name repositories", () => {
    expect(Schema.decodeUnknownSync(CommitSha)("0123456789abcdef0123456789abcdef01234567")).toBe(
      "0123456789abcdef0123456789abcdef01234567",
    );
    expect(Schema.decodeUnknownSync(RepositoryFullName)("guardian/postflight")).toBe(
      "guardian/postflight",
    );
  });

  it("rejects abbreviated SHAs and malformed repositories", () => {
    expect(() => Schema.decodeUnknownSync(CommitSha)("0123456")).toThrow();
    expect(() => Schema.decodeUnknownSync(RepositoryFullName)("postflight")).toThrow();
  });

  it.each([
    ["refs/heads/main", "BranchRef"],
    ["refs/tags/v1.0.0", "TagRef"],
    ["refs/pull/42/head", "PullRequestRef"],
    ["refs/pull/42/merge", "PullRequestRef"],
  ] as const)("parses %s algebraically", (value, tag) => {
    const parsed = parseCheckoutRef(value);
    expect(Option.isSome(parsed)).toBe(true);
    if (Option.isSome(parsed)) expect(parsed.value._tag).toBe(tag);
  });

  it("rejects ambiguous or abbreviated refs", () => {
    expect(Option.isNone(parseCheckoutRef("main"))).toBe(true);
    expect(Option.isNone(parseCheckoutRef("refs/pull/0/merge"))).toBe(true);
    expect(Option.isNone(parseCheckoutRef("refs/pull/1/test"))).toBe(true);
  });
});

describe("checkout state machine", () => {
  const phases: ReadonlyArray<CheckoutPhase> = [
    "Received",
    "Validated",
    "TargetPrepared",
    "PackReady",
    "Materialized",
    "Verified",
    "Completed",
    "Failed",
  ];

  const legal = new Set([
    "Received:Validated",
    "Received:Failed",
    "Validated:TargetPrepared",
    "Validated:Failed",
    "TargetPrepared:PackReady",
    "TargetPrepared:Failed",
    "PackReady:Materialized",
    "PackReady:Failed",
    "Materialized:Verified",
    "Materialized:Failed",
    "Verified:Completed",
    "Verified:Failed",
  ]);

  it("accepts only the declared transition table", () => {
    for (const from of phases) {
      for (const to of phases) {
        expect(isLegalTransition(from, to), `${from} -> ${to}`).toBe(legal.has(`${from}:${to}`));
      }
    }
  });

  it("keeps terminal states terminal", () => {
    expect(nextPhases("Completed")).toEqual([]);
    expect(nextPhases("Failed")).toEqual([]);
  });
});

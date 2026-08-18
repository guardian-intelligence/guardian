import { describe, expect, it } from "vitest";
import { scrubUrlQueries } from "../src/reporter.ts";

describe("scrubUrlQueries", () => {
  it("scrubs query strings from URLs", () => {
    expect(scrubUrlQueries("see https://example.com/page?token=abc&x=1 there")).toBe(
      "see https://example.com/page?[scrubbed] there",
    );
  });

  it("leaves query-free URLs alone", () => {
    const line = "navigated to https://example.com/page";
    expect(scrubUrlQueries(line)).toBe(line);
  });

  it("scrubs URLs embedded in JSON strings", () => {
    expect(scrubUrlQueries('{"url":"https://example.com/a?secret=1"}')).toBe(
      '{"url":"https://example.com/a?[scrubbed]"}',
    );
  });

  it("handles multiple URLs on one line", () => {
    const scrubbed = scrubUrlQueries("http://a.io/x?p=1 and https://b.io/y?q=2");
    expect(scrubbed).toBe("http://a.io/x?[scrubbed] and https://b.io/y?[scrubbed]");
  });
});

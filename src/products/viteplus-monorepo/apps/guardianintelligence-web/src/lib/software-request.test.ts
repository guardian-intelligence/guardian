import { describe, expect, it } from "vitest";
import {
  SOFTWARE_REQUEST_EVENT,
  softwareRequestPayload,
  validateSoftwareRequest,
} from "./software-request";

const validRequest = {
  company: "Acme, Inc.",
  email: "engineer@acme.example",
  request: "A command-line tool that verifies every release artifact before deployment.",
  softwareKind: "binary" as const,
};

describe("software request validation", () => {
  it("normalizes a valid request", () => {
    expect(
      validateSoftwareRequest({
        ...validRequest,
        company: "  Acme, Inc.  ",
        email: "  Engineer@Acme.Example ",
      }),
    ).toEqual({
      ok: true,
      value: { ...validRequest, email: "engineer@acme.example" },
    });
  });

  it("rejects missing contact, company, idea, and software type", () => {
    const result = validateSoftwareRequest({
      company: "",
      email: "not-an-email",
      request: "short",
      softwareKind: "other" as "binary",
    });
    expect(result.ok).toBe(false);
    if (!result.ok)
      expect(Object.keys(result.errors).sort()).toEqual([
        "company",
        "email",
        "request",
        "softwareKind",
      ]);
  });

  it("bounds the UTF-8 request before it reaches analytics storage", () => {
    const result = validateSoftwareRequest({ ...validRequest, request: "🪽".repeat(251) });
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.errors.request).toContain("1,000 bytes");
  });

  it("accounts for JSON escaping in the durable event limit", () => {
    const result = validateSoftwareRequest({ ...validRequest, request: "\\".repeat(1_000) });
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.errors.request).toContain("shorten");
  });
});

describe("software request payload", () => {
  it("writes one registered event with contact and request fields", () => {
    const payload = softwareRequestPayload("request-123", validRequest, {
      path: "/",
      referrer: "https://example.com/",
      sentAtUnixMs: 123,
    });

    expect(payload.sentAtUnixMs).toBe("123");
    expect(payload.events).toHaveLength(1);
    expect(payload.events[0]).toMatchObject({
      name: SOFTWARE_REQUEST_EVENT,
      path: "/",
      referrer: "https://example.com/",
    });
    expect(JSON.parse(payload.events[0]?.propsJson ?? "{}")).toEqual({
      request_id: "request-123",
      company: "Acme, Inc.",
      contact_email: "engineer@acme.example",
      software_kind: "binary",
      request: validRequest.request,
    });
  });
});

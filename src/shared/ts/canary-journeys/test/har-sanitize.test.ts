import { describe, expect, it } from "vitest";
import { sanitizeHar } from "../src/har-sanitize.ts";
import { RedactionRegistry } from "../src/redact.ts";

function fixtureHar() {
  return {
    log: {
      entries: [
        {
          request: {
            url: "https://github.com/login/oauth/authorize?client_id=abc&code=oauthcode123&state=st4te",
            headers: [
              { name: "Authorization", value: "Bearer tok_secret_value" },
              { name: "Cookie", value: "user_session=sess1on" },
              { name: "Accept", value: "text/html" },
            ],
            queryString: [
              { name: "client_id", value: "abc" },
              { name: "code", value: "oauthcode123" },
            ],
            cookies: [{ name: "user_session", value: "sess1on" }],
            postData: { text: "login=user&password=hunter2hunter2" },
          },
          response: {
            headers: [
              { name: "Set-Cookie", value: "user_session=sess1on; HttpOnly" },
              { name: "Content-Type", value: "text/html" },
            ],
            cookies: [{ name: "user_session", value: "sess1on" }],
            redirectURL: "https://guardianintelligence.org/cb?code=oauthcode123",
            content: { text: "<html>hello hunter2hunter2</html>" },
          },
        },
      ],
    },
  };
}

describe("sanitizeHar", () => {
  const registry = new RedactionRegistry();
  registry.register("password", "hunter2hunter2");

  it("strips credential carriers structurally", () => {
    const har = sanitizeHar(fixtureHar(), registry) as ReturnType<typeof fixtureHar>;
    const entry = har.log.entries[0]!;
    expect(entry.request.url).not.toContain("oauthcode123");
    expect(entry.request.headers[0]!.value).toBe("[STRIPPED]");
    expect(entry.request.headers[1]!.value).toBe("[STRIPPED]");
    expect(entry.request.headers[2]!.value).toBe("text/html");
    expect(entry.request.queryString[1]!.value).toBe("[STRIPPED]");
    expect(entry.request.cookies).toEqual([]);
    expect(entry.request.postData.text).toBe("[STRIPPED]");
    expect(entry.response.headers[0]!.value).toBe("[STRIPPED]");
    expect(entry.response.cookies).toEqual([]);
    expect(entry.response.redirectURL).not.toContain("oauthcode123");
    expect(entry.response.content.text).toBe("[STRIPPED]");
  });

  it("scrubs registered values from every remaining string", () => {
    const har = fixtureHar();
    har.log.entries[0]!.request.url = "https://example.com/?q=hunter2hunter2&code=oauthcode123";
    const sanitized = JSON.stringify(sanitizeHar(har, registry));
    expect(sanitized).not.toContain("hunter2hunter2");
    expect(sanitized).not.toContain("oauthcode123");
  });

  it("keeps non-sensitive query params and survives odd URLs", () => {
    const har = fixtureHar();
    har.log.entries[0]!.request.url = "not a url";
    const sanitized = sanitizeHar(har, registry) as ReturnType<typeof fixtureHar>;
    expect(sanitized.log.entries[0]!.request.url).toBe("not a url");
  });
});

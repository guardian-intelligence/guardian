import { describe, expect, it } from "vitest";
import { RedactionRegistry, registryFromEnv } from "../src/redact.ts";

describe("RedactionRegistry", () => {
  it("replaces every occurrence of a registered value", () => {
    const registry = new RedactionRegistry();
    registry.register("password", "hunter2hunter2");
    expect(registry.scrub("posted hunter2hunter2 then hunter2hunter2 again")).toBe(
      "posted [REDACTED:password] then [REDACTED:password] again",
    );
  });

  it("ignores empty and trivially short values", () => {
    const registry = new RedactionRegistry();
    registry.register("empty", "");
    registry.register("short", "ok");
    registry.register("missing", undefined);
    expect(registry.scrub("ok and empty stay put")).toBe("ok and empty stay put");
  });

  it("scrubs derived forms of a base32 seed", () => {
    const registry = new RedactionRegistry();
    registry.registerSeed("seed", "gezd gnbv gy3t qojq");
    expect(registry.scrub("raw gezd gnbv gy3t qojq")).toContain("[REDACTED:seed]");
    expect(registry.scrub("upper GEZDGNBVGY3TQOJQ")).toContain("[REDACTED:seed]");
    expect(registry.scrub("lower gezdgnbvgy3tqojq")).toContain("[REDACTED:seed]");
  });

  it("builds a registry from the credential env", () => {
    const registry = registryFromEnv({
      GITHUB_CANARY_PASSWORD: "sup3rs3cretvalue",
      GITHUB_CANARY_TOTP_SECRET: "GEZDGNBVGY3TQOJQ", // gitleaks:allow -- RFC 6238 public test vector
    });
    const scrubbed = registry.scrub("pw=sup3rs3cretvalue seed=GEZDGNBVGY3TQOJQ");
    expect(scrubbed).not.toContain("sup3rs3cretvalue");
    expect(scrubbed).not.toContain("GEZDGNBVGY3TQOJQ");
  });
});

import { describe, expect, it } from "vitest";
import { legalIdentity, legalIdentityJsonLd } from "./legal";

describe("public legal identity", () => {
  it("states the authoritative corporate and brand relationship", () => {
    expect(legalIdentity).toMatchObject({
      legalName: "Anveio Foundation",
      entityType: "C corporation",
      jurisdiction: "Delaware, United States",
      incorporationDate: "2026-02-06",
      publicNames: ["Guardian Intelligence", "Guardian"],
      officer: {
        name: "Shovon Hasan",
        title: "President and Chief Executive Officer",
      },
    });
  });

  it("publishes verifier-friendly structured data without private fields", () => {
    expect(legalIdentityJsonLd).toMatchObject({
      "@type": "Corporation",
      legalName: legalIdentity.legalName,
      name: "Guardian Intelligence",
      alternateName: "Guardian",
      url: legalIdentity.website,
      email: legalIdentity.contactEmail,
      foundingDate: legalIdentity.incorporationDate,
    });
    expect(legalIdentity.contactEmail.endsWith(`@${legalIdentity.domain}`)).toBe(true);
    expect(JSON.stringify(legalIdentityJsonLd)).not.toMatch(
      /streetAddress|postalCode|telephone|taxID|vatID|duns|leiCode|signature/i,
    );
  });
});

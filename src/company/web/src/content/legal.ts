export const LEGAL_META = {
  title: "Legal identity — Guardian",
  description:
    "Corporate identity and public brand disclosure for Guardian Intelligence and Guardian.",
} as const;

export const legalIdentity = {
  legalName: "Anveio Foundation",
  entityType: "C corporation",
  jurisdiction: "Delaware, United States",
  incorporationDate: "2026-02-06",
  incorporationDateLabel: "February 6, 2026",
  publicNames: ["Guardian Intelligence", "Guardian"] as const,
  officer: {
    name: "Shovon Hasan",
    title: "President and Chief Executive Officer",
  },
  domain: "guardianintelligence.org",
  website: "https://guardianintelligence.org",
  contactEmail: "hello@guardianintelligence.org",
} as const;

export const legalIdentityJsonLd = {
  "@context": "https://schema.org",
  "@type": "Corporation",
  "@id": `${legalIdentity.website}/#organization`,
  legalName: legalIdentity.legalName,
  name: legalIdentity.publicNames[0],
  alternateName: legalIdentity.publicNames[1],
  url: legalIdentity.website,
  email: legalIdentity.contactEmail,
  foundingDate: legalIdentity.incorporationDate,
  foundingLocation: {
    "@type": "AdministrativeArea",
    name: legalIdentity.jurisdiction,
  },
  employee: {
    "@type": "Person",
    name: legalIdentity.officer.name,
    jobTitle: legalIdentity.officer.title,
  },
} as const;

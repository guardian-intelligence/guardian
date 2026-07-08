// Site changelog. Distinct from the product policy changelog (which lives
// at guardianintelligence.org/postflight/policy/changelog and is the
// legal-record of commitment changes) and from the product release notes
// (which live in each product's own surface). This is the company-site
// changelog: what landed on guardianintelligence.org and when.

export interface ChangelogEntry {
  readonly date: string;
  readonly title: string;
  readonly body: string;
}

export const CHANGELOG_META = {
  title: "Changelog — Guardian",
  description: "What shipped on guardianintelligence.org and when.",
} as const;

export const changelog: readonly ChangelogEntry[] = [
  {
    date: "2026-04-29",
    title:
      "Postflight docs, policy, and console consolidate at guardianintelligence.org/postflight",
    body: "The product surface (docs, policy) and the authenticated console merge into a single TanStack Start app at guardianintelligence.org/postflight. The separate console subdomain is retired — bookmarks should point at guardianintelligence.org/postflight. Browser auth now starts, completes, and returns on guardianintelligence.org.",
  },
  {
    date: "2026-04-20",
    title: "Solutions replace Products. Trust and Legal move to Postflight.",
    body: "The public IA collapses to a single Solution — Postflight Platform — and the /products route is retired. Postflight Platform is the bundle a customer buys; services, the web console, CLIs, and SDKs are its products and are described on Postflight's own surfaces. The /trust and /legal routes are retired on guardianintelligence.org; terms, privacy, the SLA, subprocessors, data retention, and security disclosures live with Postflight at guardianintelligence.org/postflight/policy where the data is actually processed. The marketing site keeps its company-level surfaces: Letters, Design, Press, Careers, Changelog, Contact.",
  },
  {
    date: "2026-04-19",
    title: "guardianintelligence.org moves to apps/company",
    body: "The Guardian company site gets its own TanStack Start app, separate from the Postflight product surface.",
  },
];

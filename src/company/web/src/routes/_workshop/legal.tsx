import { createFileRoute } from "@tanstack/react-router";
import { BodyParagraph, PageShell } from "~/components/page-shell";
import { LEGAL_META, legalIdentity, legalIdentityJsonLd } from "~/content/legal";
import { canonicalLink, ogMeta } from "~/lib/head";

export const Route = createFileRoute("/_workshop/legal")({
  component: LegalPage,
  head: () => ({
    meta: ogMeta({
      slug: "company",
      title: LEGAL_META.title,
      description: LEGAL_META.description,
      path: "/legal",
    }),
    links: [canonicalLink("/legal")],
  }),
});

const details = [
  ["Legal entity", legalIdentity.legalName],
  ["Entity type", legalIdentity.entityType],
  ["Jurisdiction", legalIdentity.jurisdiction],
  [
    "Incorporated",
    <time key="incorporated" dateTime={legalIdentity.incorporationDate}>
      {legalIdentity.incorporationDateLabel}
    </time>,
  ],
  ["Public names", legalIdentity.publicNames.join(" and ")],
  ["Responsible officer", `${legalIdentity.officer.name}, ${legalIdentity.officer.title}`],
] as const;

function LegalPage() {
  return (
    <PageShell
      kicker="Corporate disclosure"
      heading="Guardian is operated by Anveio Foundation."
      route="/legal"
    >
      <script
        type="application/ld+json"
        dangerouslySetInnerHTML={{
          __html: JSON.stringify(legalIdentityJsonLd).replaceAll("<", "\\u003c"),
        }}
      />

      <BodyParagraph>
        Anveio Foundation is the legal entity behind Guardian Intelligence and Guardian. The
        corporation does business publicly under those names.
      </BodyParagraph>

      <section aria-labelledby="corporate-identity" className="mt-8">
        <h2
          id="corporate-identity"
          className="font-mono text-[11px] font-medium uppercase tracking-[0.16em]"
          style={{ color: "var(--treatment-muted-faint)" }}
        >
          Corporate identity
        </h2>
        <dl className="mt-4 divide-y" style={{ borderColor: "var(--treatment-muted-faint)" }}>
          {details.map(([label, value]) => (
            <div
              key={label}
              className="grid gap-2 py-4 sm:grid-cols-[10rem_1fr] sm:gap-6"
              style={{ borderColor: "var(--treatment-muted-faint)" }}
            >
              <dt
                className="font-mono text-[10px] uppercase tracking-[0.16em]"
                style={{ color: "var(--treatment-muted-faint)" }}
              >
                {label}
              </dt>
              <dd
                style={{
                  color: "var(--treatment-ink)",
                  fontFamily: "'Geist', sans-serif",
                  fontSize: "15px",
                  lineHeight: 1.5,
                }}
              >
                {value}
              </dd>
            </div>
          ))}
        </dl>
      </section>

      <section aria-labelledby="brand-relationship" className="mt-8 flex flex-col gap-3">
        <h2
          id="brand-relationship"
          className="font-mono text-[11px] font-medium uppercase tracking-[0.16em]"
          style={{ color: "var(--treatment-muted-faint)" }}
        >
          Brand relationship
        </h2>
        <BodyParagraph>
          Guardian Intelligence and Guardian are public names used by Anveio Foundation. They do not
          identify separate legal entities. Anveio Foundation is the contracting party for business
          conducted under either name.
        </BodyParagraph>
      </section>

      <section aria-labelledby="official-contact" className="mt-8 flex flex-col gap-3">
        <h2
          id="official-contact"
          className="font-mono text-[11px] font-medium uppercase tracking-[0.16em]"
          style={{ color: "var(--treatment-muted-faint)" }}
        >
          Official contact
        </h2>
        <BodyParagraph>
          The official website is{" "}
          <a href={legalIdentity.website} className="underline underline-offset-4">
            {legalIdentity.domain}
          </a>
          . Corporate identity questions may be sent to{" "}
          <a href={`mailto:${legalIdentity.contactEmail}`} className="underline underline-offset-4">
            {legalIdentity.contactEmail}
          </a>
          .
        </BodyParagraph>
      </section>
    </PageShell>
  );
}

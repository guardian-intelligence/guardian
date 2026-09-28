import { createFileRoute } from "@tanstack/react-router";
import { CONTACT_META, contact } from "~/content/contact";
import { BodyParagraph, PageShell } from "~/components/page-shell";
import { ogMeta } from "~/lib/head";

export const Route = createFileRoute("/_workshop/contact")({
  component: ContactPage,
  head: () => ({
    meta: ogMeta({
      slug: "contact",
      title: CONTACT_META.title,
      description: CONTACT_META.description,
    }),
  }),
});

function ContactPage() {
  return (
    <PageShell kicker={contact.kicker} heading={contact.hero}>
      <BodyParagraph>{contact.intro}</BodyParagraph>

      <a
        href={`mailto:${contact.email}`}
        className="mt-6"
        style={{
          color: "var(--treatment-ink)",
          fontSize: "16px",
          textDecoration: "underline",
          textDecorationThickness: "1px",
          textUnderlineOffset: "4px",
        }}
      >
        {contact.email}
      </a>

      <p
        className="mt-6 font-mono text-[11px] uppercase tracking-[0.16em]"
        style={{ color: "var(--treatment-muted-faint)" }}
      >
        {contact.mailingAddress}
      </p>
    </PageShell>
  );
}

import { createFileRoute } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { PageShell } from "~/components/page-shell";

// noindex while the encrypted-volume path is in development; flip to ogMeta +
// sitemap when it ships.
export const Route = createFileRoute("/_workshop/postflight/docs")({
  component: PostflightDocsPage,
  head: () => ({
    meta: [
      { title: "Postflight security — Guardian Intelligence" },
      {
        name: "description",
        content:
          "Postflight runs your CI inside confidential VMs. Verify the environment yourself — trust, but verify.",
      },
      { name: "robots", content: "noindex" },
    ],
  }),
});

function PostflightDocsPage() {
  return (
    <PageShell kicker="Postflight · Docs" heading="Trust, but verify.">
      <Body>
        Your workload boots in a secure VM running our thin and light guest agent, designed to
        ensure your workload&apos;s environment is secure before your code runs. It:
      </Body>
      <Item>
        Runs basic checks on your behalf. You don&apos;t have to trust them — verify the guest agent
        is the one we published, and verify the environment yourself, from your own workflow:
      </Item>
      <Commands />
      <Item>
        Verifies the attached encrypted volumes — cached from your previous successful runs — pass
        an integrity check. Your data is unreadable ciphertext, but a compromised host could corrupt
        it before attaching the volume. A failed check discards the cache and your job runs cold:
        corruption can cost speed, never confidentiality.
      </Item>
      <Body>
        These protections are designed to keep your workload and data safe even if the entire
        Guardian platform is compromised — as long as you do your part to keep yourself safe. Trust,
        but verify.
      </Body>
      <Notice>
        Design preview: the encrypted-volume path is in active development, and the exact verify
        invocations ship with it.
      </Notice>
    </PageShell>
  );
}

function Body({ children }: { children: ReactNode }) {
  return (
    <p
      style={{
        fontFamily: "'Geist', sans-serif",
        fontSize: "16px",
        lineHeight: 1.55,
        color: "var(--treatment-muted-strong)",
        margin: 0,
      }}
    >
      {children}
    </p>
  );
}

function Item({ children }: { children: ReactNode }) {
  return (
    <p
      className="border-l pl-4"
      style={{
        borderColor: "var(--treatment-surface-border)",
        fontFamily: "'Geist', sans-serif",
        fontSize: "15px",
        lineHeight: 1.55,
        color: "var(--treatment-muted-strong)",
        margin: 0,
      }}
    >
      {children}
    </p>
  );
}

function Commands() {
  return (
    <pre
      className="overflow-x-auto rounded-md p-4 font-mono text-[12.5px] leading-relaxed"
      style={{
        background: "var(--treatment-surface-subtle)",
        border: "1px solid var(--treatment-surface-border)",
        color: "var(--treatment-muted)",
        margin: 0,
      }}
    >
      {`cosign verify ghcr.io/guardian-intelligence/postflight-guestd@<digest>
postflight attest --verify  # the running VM proves it is a genuine SEV-SNP guest`}
    </pre>
  );
}

function Notice({ children }: { children: ReactNode }) {
  return (
    <p
      className="border-l-2 py-1 pl-4"
      style={{
        borderColor: "var(--treatment-accent)",
        fontFamily: "'Geist', sans-serif",
        fontSize: "13px",
        lineHeight: 1.55,
        color: "var(--treatment-muted-meta)",
        margin: 0,
      }}
    >
      {children}
    </p>
  );
}

import { createFileRoute } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { PageShell } from "~/components/page-shell";
import {
  COROLLARIES,
  HONEST_LIMITS,
  LIFECYCLE,
  LIFECYCLE_INVARIANT,
  OBLIGATIONS,
  POSTFLIGHT_DOCS_META,
  STATUS_LABEL,
  type MechanismStatus,
} from "~/content/postflight-docs";

// noindex while the architecture is a design preview; flip to ogMeta + sitemap
// when the at-rest sealing obligation reaches "live".
export const Route = createFileRoute("/_workshop/postflight/docs")({
  component: PostflightDocsPage,
  head: () => ({
    meta: [
      { title: POSTFLIGHT_DOCS_META.title },
      { name: "description", content: POSTFLIGHT_DOCS_META.description },
      { name: "robots", content: "noindex" },
    ],
  }),
});

const STATUS_COLOR: Record<MechanismStatus, string> = {
  live: "var(--treatment-accent)",
  partial: "var(--treatment-muted)",
  design: "var(--treatment-muted-faint)",
};

function PostflightDocsPage() {
  return (
    <PageShell
      kicker="Postflight · Docs"
      heading="What our host can see. State by state."
    >
      <BodyText>
        Postflight runs CI jobs inside AMD SEV-SNP confidential VMs. This page walks the
        lifecycle of one job as a state machine and answers, at every state, the only
        question that matters: what can the machine&apos;s operator — Guardian — actually
        see?
      </BodyText>
      <Notice>
        Design preview. This documents the target architecture, and every mechanism
        carries its implementation status. We publish before it is finished on purpose:
        a claim you can&apos;t check isn&apos;t a claim, and checking starts with reading.
      </Notice>
      <StatusLegend />

      <SectionHeading>The lifecycle</SectionHeading>
      <ol className="flex flex-col" style={{ listStyle: "none", margin: 0, padding: 0 }}>
        {LIFECYCLE.map((state) => (
          <li
            key={state.id}
            className="flex flex-col gap-2 border-l py-5 pl-5"
            style={{ borderColor: "var(--treatment-surface-border)" }}
          >
            <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
              <span
                className="font-mono text-[11px] tracking-[0.12em]"
                style={{ color: "var(--treatment-muted-faint)" }}
              >
                {state.id}
              </span>
              <span
                style={{
                  fontFamily: "'Geist', sans-serif",
                  fontWeight: 600,
                  fontSize: "17px",
                  letterSpacing: "-0.012em",
                  color: "var(--treatment-ink)",
                }}
              >
                {state.name}
              </span>
              <StatusChip status={state.status} />
            </div>
            <p
              style={{
                fontFamily: "'Geist', sans-serif",
                fontSize: "15px",
                lineHeight: 1.55,
                color: "var(--treatment-muted-strong)",
                margin: 0,
              }}
            >
              {state.summary}
            </p>
            <DetailLine label="Host sees">{state.hostSees}</DetailLine>
            {state.onFailure ? (
              <DetailLine label="If it fails">{state.onFailure}</DetailLine>
            ) : null}
          </li>
        ))}
      </ol>
      <Notice>{LIFECYCLE_INVARIANT}</Notice>

      <SectionHeading>What falls out of the key derivation</SectionHeading>
      {COROLLARIES.map((item) => (
        <Entry key={item.heading} heading={item.heading} body={item.body} />
      ))}

      <SectionHeading>What this does not cover</SectionHeading>
      {HONEST_LIMITS.map((item) => (
        <Entry key={item.heading} heading={item.heading} body={item.body} />
      ))}

      <SectionHeading>Where this stands today</SectionHeading>
      <ul className="flex flex-col gap-4" style={{ listStyle: "none", margin: 0, padding: 0 }}>
        {OBLIGATIONS.map((row) => (
          <li
            key={row.obligation}
            className="flex flex-col gap-1 border-t pt-4"
            style={{ borderColor: "var(--treatment-surface-border)" }}
          >
            <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
              <span
                style={{
                  fontFamily: "'Geist', sans-serif",
                  fontWeight: 500,
                  fontSize: "15px",
                  color: "var(--treatment-ink)",
                }}
              >
                {row.obligation}
              </span>
              <StatusChip status={row.status} />
            </div>
            <p
              style={{
                fontFamily: "'Geist', sans-serif",
                fontSize: "14px",
                lineHeight: 1.55,
                color: "var(--treatment-muted-meta)",
                margin: 0,
              }}
            >
              {row.detail}
            </p>
          </li>
        ))}
      </ul>
    </PageShell>
  );
}

function SectionHeading({ children }: { children: ReactNode }) {
  return (
    <h2
      className="mt-8"
      style={{
        fontFamily: "'Geist', sans-serif",
        fontWeight: 600,
        fontSize: "24px",
        lineHeight: 1.15,
        letterSpacing: "-0.016em",
        color: "var(--treatment-ink)",
        margin: 0,
      }}
    >
      {children}
    </h2>
  );
}

function BodyText({ children }: { children: ReactNode }) {
  return (
    <p
      style={{
        fontFamily: "'Geist', sans-serif",
        fontSize: "16px",
        lineHeight: 1.55,
        color: "var(--treatment-muted)",
        margin: 0,
      }}
    >
      {children}
    </p>
  );
}

function Notice({ children }: { children: ReactNode }) {
  return (
    <p
      className="border-l-2 py-1 pl-4"
      style={{
        borderColor: "var(--treatment-accent)",
        fontFamily: "'Geist', sans-serif",
        fontSize: "14px",
        lineHeight: 1.55,
        color: "var(--treatment-muted-strong)",
        margin: 0,
      }}
    >
      {children}
    </p>
  );
}

function StatusChip({ status }: { status: MechanismStatus }) {
  return (
    <span
      className="font-mono text-[10px] uppercase tracking-[0.16em]"
      style={{ color: STATUS_COLOR[status] }}
    >
      {STATUS_LABEL[status]}
    </span>
  );
}

function StatusLegend() {
  return (
    <p
      className="font-mono text-[11px] tracking-[0.06em]"
      style={{ color: "var(--treatment-muted-meta)", margin: 0 }}
    >
      <StatusChip status="live" /> running in production today ·{" "}
      <StatusChip status="partial" /> mechanism proven, not fully wired ·{" "}
      <StatusChip status="design" /> designed, not built
    </p>
  );
}

function DetailLine({ label, children }: { label: string; children: ReactNode }) {
  return (
    <p className="flex flex-col gap-0.5 md:flex-row md:items-baseline md:gap-3" style={{ margin: 0 }}>
      <span
        className="font-mono text-[10px] uppercase tracking-[0.16em] md:w-24 md:shrink-0"
        style={{ color: "var(--treatment-muted-faint)" }}
      >
        {label}
      </span>
      <span
        style={{
          fontFamily: "'Geist', sans-serif",
          fontSize: "13px",
          lineHeight: 1.55,
          color: "var(--treatment-muted-meta)",
        }}
      >
        {children}
      </span>
    </p>
  );
}

function Entry({ heading, body }: { heading: string; body: string }) {
  return (
    <div className="flex flex-col gap-1.5">
      <p
        style={{
          fontFamily: "'Geist', sans-serif",
          fontWeight: 500,
          fontSize: "15px",
          color: "var(--treatment-ink)",
          margin: 0,
        }}
      >
        {heading}
      </p>
      <p
        style={{
          fontFamily: "'Geist', sans-serif",
          fontSize: "14px",
          lineHeight: 1.55,
          color: "var(--treatment-muted-strong)",
          margin: 0,
        }}
      >
        {body}
      </p>
    </div>
  );
}

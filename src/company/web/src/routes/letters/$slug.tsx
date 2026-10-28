import { createFileRoute, Link, notFound, useSearch } from "@tanstack/react-router";
import { createServerFn } from "@tanstack/react-start";
import { ArrowLeft } from "lucide-react";
import { useEffect } from "react";
import * as v from "valibot";
import type { Letter } from "~/content/letters";
import {
  publishedLetterBySlug,
  setLettersEdgeCacheHeader,
  setUncacheableHeader,
} from "~/content/letters.server";
import { LetterSignature } from "~/features/letters/letter-signature";
import {
  LETTER_POST_PAGE_PADDING_CLASS,
  LETTER_READING_COLUMN_CLASS,
  LETTER_TEXT_MEASURE_CLASS,
  excerptOf,
  LetterBody,
  LetterDate,
  LetterSalutation,
} from "~/features/letters/typography";
import {
  LETTER_RETURN_VIEW_TRANSITION,
  letterNavigationIntentHandlers,
} from "~/features/letters/transitions.intent";
import {
  fixedTransitionStyle,
  LETTER_RETURN_TRANSITION_NAME,
} from "~/features/letters/transitions";
import { LetterOgPreview, LetterOgPreviewHotkey } from "~/features/letters/og-preview";
import { canonicalLink, ogMeta, SITE_URL } from "~/lib/head";
import { emitSpan } from "@guardian/telemetry";

// Structured provenance for the record, invisible to the reader. A letter's
// page can stay exactly as open-ended as it was written while the head
// carries a JSON-LD account of what the work is: its real author, the
// author's own note of context, and the distinction between the date the
// letter wears (its dateline may be set in another time) and the date it was
// published. Crawlers, archives, and search parse this; no reader sees it.
function letterJsonLd(letter: Letter) {
  if (!letter.note && !letter.author && !letter.description) {
    return undefined;
  }
  const data: Record<string, unknown> = {
    "@context": "https://schema.org",
    "@type": "CreativeWork",
    name: letter.title,
    url: `${SITE_URL}/letters/${letter.slug}`,
    inLanguage: "en",
    // The frontmatter date is the letter's dateline — the date the letter
    // wears in its own world, not necessarily when it was written.
    temporalCoverage: letter.publishedAt,
  };
  if (letter.author) {
    const person: Record<string, unknown> = { "@type": "Person", name: letter.author };
    if (letter.authorTitle) {
      person.jobTitle = letter.authorTitle;
    }
    data.author = person;
  }
  if (letter.description) {
    data.description = letter.description;
  }
  if (letter.note) {
    data.abstract = letter.note;
  }
  return [
    {
      type: "application/ld+json",
      children: JSON.stringify(data),
    },
  ];
}

// A single letter. The form follows DESIGN.md: the date sits at the very
// top, left-aligned to the column, sized to exactly two ruled pitches
// (2 × 32px — see linePitch in features/letters/fonts.ts, which the graph
// itself derives from) the way it was always written by hand. The title is the
// salutation — "Dear Shovon," — and renders directly under the date, in the
// body's hand, the way a letter is actually addressed. The body opens
// underneath on the same measure as the index preview, so browser view
// transitions have stable type geometry to interpolate. The body closes on
// "Love,"; the name is not signed but redacted — a solid marker bar, four
// characters wide, struck where it would be. The frontmatter title doubles as
// the <head>/OG title.

const letterLoader = createServerFn({ method: "GET" })
  .validator((slug: unknown) => v.parse(v.string(), slug))
  .handler(async ({ data: slug }) => {
    const letter = await publishedLetterBySlug(slug);
    // A miss must not become a cached 404: a just-published letter's URL
    // would otherwise stay dead at the edge for the whole TTL.
    if (letter) setLettersEdgeCacheHeader();
    else setUncacheableHeader();
    return { letter: letter ?? null };
  });

export const Route = createFileRoute("/letters/$slug")({
  component: LetterPost,
  loader: async ({ params }) => {
    const { letter } = await letterLoader({ data: params.slug });
    if (!letter) {
      throw notFound();
    }
    return { letter };
  },
  head: ({ loaderData }) => {
    const letter = loaderData?.letter;
    if (!letter) {
      return { meta: [{ title: "Letters — Guardian" }] };
    }
    return {
      meta: ogMeta({
        slug: `letter/${letter.slug}`,
        title: `${letter.title} — Guardian`,
        // The provenance description doubles as the share text: what the
        // link says about itself on X, in search, and in previews. Letters
        // that declare none are described by their own opening words.
        description: letter.description || excerptOf(letter.bodyHtml),
        type: "article",
        path: `/letters/${letter.slug}`,
        imageFormat: "png",
      }),
      links: [canonicalLink(`/letters/${letter.slug}`)],
      scripts: letterJsonLd(letter),
    };
  },
});

function LetterPost() {
  const { letter } = Route.useLoaderData();
  const search = useSearch({ strict: false }) as {
    readonly developmentMode?: string | number;
    readonly og?: string | number;
  };
  // Developer affordance: Cmd+D arms developer mode, then "s" swaps the article
  // for the OG card (PNG + source SVG). The article isn't rendered while on.
  // (TanStack parses ?og=1 to the number 1, so coerce before comparing.)
  const showOgPreview = String(search.developmentMode) === "1" && String(search.og) === "1";

  useEffect(() => {
    emitSpan("company.letter.view", {
      "letter.slug": letter.slug,
      "letter.published_at": letter.publishedAt,
    });
  }, [letter.slug, letter.publishedAt]);

  return (
    <article
      data-letter-transition-route="post"
      className={`${LETTER_READING_COLUMN_CLASS} ${LETTER_POST_PAGE_PADDING_CLASS}`}
    >
      <LetterOgPreviewHotkey />
      {showOgPreview ? (
        <LetterOgPreview letter={letter} />
      ) : (
        <div className={LETTER_TEXT_MEASURE_CLASS} data-letter-entry={letter.slug}>
          <LetterReturnLink />
          <LetterDate letter={letter} />
          <LetterSalutation letter={letter} />
          <LetterBody letter={letter} />
          <LetterSignature letter={letter} />
        </div>
      )}
    </article>
  );
}

function LetterReturnLink() {
  return (
    <Link
      to="/letters"
      activeOptions={{ exact: true }}
      viewTransition={LETTER_RETURN_VIEW_TRANSITION}
      {...letterNavigationIntentHandlers("return")}
      data-letter-return
      aria-label="Return to letters"
      className="-mx-3 mb-2 inline-flex min-h-11 items-center gap-2 px-3 font-mono text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--treatment-muted-meta)] no-underline outline-none focus-visible:ring-2 focus-visible:ring-[var(--treatment-rule-color)] focus-visible:ring-offset-4 focus-visible:ring-offset-[var(--treatment-ground)]"
      style={fixedTransitionStyle(LETTER_RETURN_TRANSITION_NAME)}
    >
      <ArrowLeft aria-hidden="true" size={13} strokeWidth={1.75} />
      <span>Return</span>
    </Link>
  );
}

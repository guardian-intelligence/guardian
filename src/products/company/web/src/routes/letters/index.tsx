import { createFileRoute, Link } from "@tanstack/react-router";
import { LETTERS_META, sortedLetters, type Letter } from "~/content/letters";
import {
  LETTER_OPEN_VIEW_TRANSITION,
  letterNavigationIntentHandlers,
} from "~/features/letters/transitions.intent";
import {
  excerptOf,
  LETTER_INDEX_PAGE_PADDING_CLASS,
  LETTER_READING_COLUMN_CLASS,
  LETTER_TEXT_MEASURE_CLASS,
  LetterDate,
  LetterExcerpt,
  LetterSalutation,
} from "~/features/letters/typography";
import { ogMeta } from "~/lib/head";

// Letters index. Each entry is a piece of correspondence on the page: a
// dated sheet opening: date first, salutation second, then the first words
// dissolving back into the paper. The reader sees enough to know whether to
// open it, never a summary written *about* it.
// The excerpt is the letter's real first words (the frontmatter summary is
// SEO/OG only and never renders). A letter with an empty body is one that
// has been dated and titled but not yet written; it shows the title alone
// rather than a faked preview.

export const Route = createFileRoute("/letters/")({
  component: LettersIndex,
  head: () => ({
    meta: ogMeta({
      slug: "letters",
      title: LETTERS_META.title,
      description: LETTERS_META.description,
    }),
  }),
});

function LettersIndex() {
  const letters = sortedLetters();

  return (
    <div
      data-letter-transition-route="index"
      className={`${LETTER_READING_COLUMN_CLASS} ${LETTER_INDEX_PAGE_PADDING_CLASS}`}
    >
      <ul className={LETTER_TEXT_MEASURE_CLASS}>
        {letters.map((letter, index) => (
          <li key={letter.slug}>
            <LetterEntry letter={letter} morph={index === 0} />
          </li>
        ))}
      </ul>
    </div>
  );
}

// Only the top-most sheet morphs into the letter page: a morph from an entry
// lower on the index tweens its slots across the whole viewport, which reads
// as the sheet flying rather than settling. The rest of the entries hand off
// through the plain root crossfade.
function LetterEntry({ letter, morph }: { letter: Letter; morph: boolean }) {
  const excerpt = excerptOf(letter.leadHtml || letter.bodyHtml);

  return (
    <Link
      to="/letters/$slug"
      params={{ slug: letter.slug }}
      viewTransition={LETTER_OPEN_VIEW_TRANSITION}
      {...letterNavigationIntentHandlers("open")}
      data-letter-entry={letter.slug}
      className="block py-14 no-underline outline-none focus-visible:ring-2 focus-visible:ring-[var(--treatment-rule-color)] focus-visible:ring-offset-4 focus-visible:ring-offset-[var(--treatment-ground)] sm:py-16"
    >
      <div style={{ margin: 0 }}>
        <LetterDate letter={letter} scale="index" morph={morph} />
        <LetterSalutation letter={letter} morph={morph} />
        <LetterExcerpt letter={letter} excerpt={excerpt} morph={morph} />
        <div className="mt-8 flex justify-end">
          <span className="font-mono text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--treatment-muted-meta)] underline decoration-[var(--treatment-rule-color)] decoration-[1px] underline-offset-[6px]">
            Read letter
          </span>
        </div>
      </div>
    </Link>
  );
}

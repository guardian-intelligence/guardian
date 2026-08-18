import type { Letter } from "~/content/letters";
import { currentNewsroomItem } from "~/content/newsroom";
import { excerptOf, formatLetterDate, formatLetterSalutation } from "~/features/letters/typography";
import type { OGSpec } from "./template";

// OG card catalog. Keyed by slug. Every public route looks up its spec by
// slug and hands it to buildOGCard(). Adding a card = appending an entry here
// with a title and a flare word that appears in the title. buildOGCard()
// validates each title before render so banned words fail loudly at request
// time instead of reaching the share preview.
//
// Letters are NOT enumerated here — letterOgSpec() synthesizes an OGSpec
// from letter data, and the server-side OG handler resolves the
// "letter/<slug>" namespace against the runtime letters source. Publishing a
// letter in the Studio serves its card with no catalog edit.

const currentBulletin = currentNewsroomItem();

export const OG_CATALOG: Record<string, OGSpec> = {
  home: {
    slug: "home",
    title: "We ship the reference architecture every founder needs.",
    flare: "architecture",
    footerLeft: "guardianintelligence.org",
    footerRight: "Seattle · 2026",
  },
  design: {
    slug: "design",
    title: "The Guardian brand system.",
    flare: "Guardian",
    footerLeft: "guardianintelligence.org/design",
    footerRight: "Seattle · 2026",
  },
  letters: {
    treatment: "letters",
    slug: "letters",
    title: "Letters",
    flare: "Long-form",
    kicker: "Long-form from Guardian.",
    subtitle: "Published when we have something to say, not on a calendar.",
    footerLeft: "guardianintelligence.org/letters",
    footerRight: "Seattle · 2026",
  },
  news: {
    treatment: "newsroom",
    slug: "news",
    title: currentBulletin?.title ?? "Bulletins from Guardian.",
    flare: "Bulletins",
    kicker: currentBulletin ? `${currentBulletin.kicker} · ${currentBulletin.date}` : "News",
    subtitle: currentBulletin?.deck ?? "News, milestones, and public notes from Guardian.",
    footerLeft: "guardianintelligence.org/news",
    footerRight: "Seattle · 2026",
  },
  solutions: {
    slug: "solutions",
    title: "One house, one platform.",
    flare: "one platform",
    footerLeft: "guardianintelligence.org/solutions",
    footerRight: "Seattle · 2026",
  },
  company: {
    slug: "company",
    title: "An American applied intelligence firm.",
    flare: "firm",
    footerLeft: "guardianintelligence.org/company",
    footerRight: "Seattle · 2026",
  },
  careers: {
    slug: "careers",
    title: "We hire slowly.",
    flare: "slowly",
    footerLeft: "guardianintelligence.org/careers",
    footerRight: "Seattle · 2026",
  },
  contact: {
    slug: "contact",
    title: "We answer every note.",
    flare: "every note",
    footerLeft: "guardianintelligence.org/contact",
    footerRight: "Seattle · 2026",
  },
  press: {
    slug: "press",
    title: "The brand, on the record.",
    flare: "on the record",
    footerLeft: "guardianintelligence.org/press",
    footerRight: "Seattle · 2026",
  },
  changelog: {
    slug: "changelog",
    title: "What shipped, when.",
    flare: "shipped",
    footerLeft: "guardianintelligence.org/changelog",
    footerRight: "Seattle · 2026",
  },
};

export const LETTER_SLUG_PREFIX = "letter/";

// Pure letter → OGSpec mapping; the caller supplies the letter so this stays
// usable from the client (the OG preview overlay) without pulling letter data
// into the bundle.
export function letterOgSpec(letter: Letter): OGSpec {
  return {
    treatment: "letters",
    slug: `${LETTER_SLUG_PREFIX}${letter.slug}`,
    title: formatLetterSalutation(letter),
    flare: letter.flare,
    kicker: formatLetterDate(letter.publishedAt),
    bodyExcerpt: excerptOf(letter.bodyHtml),
    ...(letter.summary === letter.title ? {} : { subtitle: letter.summary }),
    footerLeft: `guardianintelligence.org/letters/${letter.slug}`,
    footerRight: "Read the letter →",
  };
}

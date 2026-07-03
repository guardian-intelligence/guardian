// The ink of the letters treatment: a gel pen laying down slightly different
// amounts of ink on every word. Each word gets a deterministic "ink bucket" —
// a barely perceptible pairing of pressure (Crimson Pro's wght axis, ±~15
// around the 500 body weight), ink density (opacity), baseline drift (a word
// sitting a fraction of a pixel off the ruled line), and rhythm (letter-
// spacing breathing in and out). The variation is a
// pure hash of (slug, word index) — the same referentially transparent
// pick-by-label scheme as the paper's seeded boundary in routes/letters/
// route.tsx — so SSR, hydration, and every rebuild lay down identical ink.
// (Build determinism is load-bearing: the image digest pin in
// deployments/company/prod/web.yaml is verified against a CI rebuild.)
//
// Two writers share this module:
//   - the company:letters-markdown Vite plugin wraps the words of the
//     rendered letter HTML at build time (inkWrapHtml)
//   - LetterExcerpt wraps the index preview at render time (see ink-text.tsx),
//     counting words from 0 exactly like the lead it opens into, so the same
//     words wear the same ink on both sides of the view transition.

export interface InkBucket {
  // Crimson Pro wght axis value; centred on the 500 body weight.
  readonly wght: number;
  // Ink density. Correlated with wght: a heavier stroke laid more ink.
  readonly opacity: number;
  // Baseline drift, px. Applied as position:relative top — relative offsets
  // move plain inline boxes, so a word can sit a hair above or below the
  // ruled line without becoming inline-block (which would change line
  // breaking). Sub-half-pixel: one device pixel at 2x, atmosphere at 1x.
  readonly baselineShift: number;
  // Letter-spacing drift, em — the hand's rhythm tightening and loosening
  // within a word. At body size the extreme is ~0.16px per letter gap.
  readonly tracking: number;
}

// Eight steps keep the class vocabulary tiny (one short class per word in the
// shipped HTML) while the wght axis interpolates smoothly between them. The
// span is deliberately narrow — reading must never snag on a single word.
// Drift and tracking are shuffled against the wght ramp (heavy words are not
// systematically low or tight), and both columns sum to ~0 so a line of many
// words still averages onto the ruled baseline at its designed rhythm.
export const INK_BUCKETS: readonly InkBucket[] = [
  { wght: 489, opacity: 0.97, baselineShift: 0.16, tracking: -0.004 },
  { wght: 493, opacity: 0.975, baselineShift: -0.24, tracking: 0.006 },
  { wght: 496, opacity: 0.98, baselineShift: 0.29, tracking: 0.002 },
  { wght: 500, opacity: 0.985, baselineShift: -0.08, tracking: -0.006 },
  { wght: 503, opacity: 0.988, baselineShift: 0.05, tracking: 0.008 },
  { wght: 507, opacity: 0.992, baselineShift: -0.3, tracking: -0.002 },
  { wght: 511, opacity: 0.996, baselineShift: 0.21, tracking: 0.004 },
  { wght: 515, opacity: 1, baselineShift: -0.14, tracking: -0.007 },
];

export const INK_CLASS_PREFIX = "letter-ink-";

// FNV-1a, 32-bit — same shape as the paper-boundary hash in routes/letters/
// route.tsx. Math.imul keeps it a true 32-bit multiply across engines.
function fnv1a(s: string): number {
  let h = 2166136261 >>> 0;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

// Word index → bucket, keyed by index alone (not the word's text) so the
// index excerpt and the letter body agree even where HTML entities make the
// same word spell differently in markup and in plain text.
export function inkBucketIndex(slug: string, wordIndex: number): number {
  return fnv1a(`${slug}:ink:${wordIndex}`) % INK_BUCKETS.length;
}

export function inkClassName(slug: string, wordIndex: number): string {
  return `${INK_CLASS_PREFIX}${inkBucketIndex(slug, wordIndex)}`;
}

// CSS for the buckets, scoped to the letters treatment. font-variation-settings
// addresses the wght axis directly, below font-weight in the cascade's
// font-matching, so the buckets ride on top of --letters-body-weight without
// fighting it anywhere else.
export function inkClassRules(scope: string): string {
  return INK_BUCKETS.map(
    (b, i) =>
      `${scope} .${INK_CLASS_PREFIX}${i}{font-variation-settings:'wght' ${b.wght};opacity:${b.opacity};position:relative;top:${b.baselineShift}px;letter-spacing:${b.tracking}em;}`,
  ).join("");
}

// Wrap every word of rendered letter HTML in an ink span. Only text between
// tags is touched — tags, attributes, and whitespace pass through byte-for-
// byte — and the word counter runs across the whole fragment in document
// order. Runs at build time in the company:letters-markdown Vite plugin.
export function inkWrapHtml(html: string, slug: string): string {
  let wordIndex = 0;
  return html
    .split(/(<[^>]*>)/)
    .map((segment) => {
      if (segment.startsWith("<") || segment === "") return segment;
      return segment.replace(
        /\S+/g,
        (word) => `<span class="${inkClassName(slug, wordIndex++)}">${word}</span>`,
      );
    })
    .join("");
}

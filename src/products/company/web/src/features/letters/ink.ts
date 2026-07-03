// The ink of the letters treatment: a gel pen laying down slightly different
// amounts of ink on every word. Each word gets a deterministic "ink bucket" —
// a barely perceptible pairing of pressure (Crimson Pro's wght axis, ±~15
// around the 500 body weight) and ink density (opacity) — and a "flow class"
// that carries the hand's baseline wander and word-gap rhythm. All of it is a
// pure hash read of (slug, word index) — the same referentially transparent
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
}

// Eight steps keep the class vocabulary tiny (one short class per word in the
// shipped HTML) while the wght axis interpolates smoothly between them. The
// span is deliberately narrow — reading must never snag on a single word.
export const INK_BUCKETS: readonly InkBucket[] = [
  { wght: 489, opacity: 0.97 },
  { wght: 493, opacity: 0.975 },
  { wght: 496, opacity: 0.98 },
  { wght: 500, opacity: 0.985 },
  { wght: 503, opacity: 0.988 },
  { wght: 507, opacity: 0.992 },
  { wght: 511, opacity: 0.996 },
  { wght: 515, opacity: 1 },
];

export const INK_CLASS_PREFIX = "letter-ink-";
export const FLOW_CLASS_PREFIX = "letter-flow-";

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

// pick: slug → label → Unit ∈ [0,1). A pure hash read, not a stateful RNG.
const pickUnit = (slug: string, label: string): number =>
  fnv1a(`${slug}:${label}`) / 4294967296;

// Word index → ink bucket, keyed by index alone (not the word's text) so the
// index excerpt and the letter body agree even where HTML entities make the
// same word spell differently in markup and in plain text. Pressure genuinely
// is per-dip, so independent per-word assignment is the right model here —
// unlike position, where independence reads as a rendering glitch (below).
export function inkBucketIndex(slug: string, wordIndex: number): number {
  return fnv1a(`${slug}:ink:${wordIndex}`) % INK_BUCKETS.length;
}

export function inkClassName(slug: string, wordIndex: number): string {
  return `${INK_CLASS_PREFIX}${inkBucketIndex(slug, wordIndex)}`;
}

// --- The flow curve -------------------------------------------------------
//
// A hand's baseline wander is continuous — each word's position is highly
// correlated with its neighbour's, because the hand is a physical system
// being dragged across the page, not a random-number generator. (Independent
// per-word offsets shipped first and read exactly like white noise does: one
// word visibly sunk between two level neighbours, i.e. a glitch.) So the
// vertical drift is a smooth curve — a seeded sum of four sine octaves —
// sampled at each word's index. Reading order is the only reflow-proof
// domain: within a line, index is monotone in x, so smooth-in-index is
// smooth-along-the-line at every viewport width, and the curve simply
// continues across the wrap.
//
// Periods are seeded per slug around 11 / 23 / 47 / 110 words: the fastest is
// about one written line of the reading column, the slowest spans paragraphs
// — the hand settling differently over the course of a long letter. The
// amplitude keeps the wander to a fraction of the ruled pitch: on the
// reference pages the hand holds the graph line and wobbles about it; the
// line itself stays true.
const FLOW_MAX = 0.5; // px — the wander-amplitude knob
const FLOW_STEPS = 16;

interface FlowOctave {
  readonly period: number;
  readonly amp: number;
  readonly phase: number;
}

const FLOW_OCTAVE_SPEC: ReadonlyArray<readonly [basePeriod: number, amp: number]> = [
  [11, 0.5],
  [23, 0.28],
  [47, 0.14],
  [110, 0.08],
];

const TAU = Math.PI * 2;

function flowOctaves(slug: string): FlowOctave[] {
  return FLOW_OCTAVE_SPEC.map(([basePeriod, amp], k) => ({
    period: basePeriod * (0.8 + 0.4 * pickUnit(slug, `flow.T${k}`)),
    amp,
    phase: TAU * pickUnit(slug, `flow.phi${k}`),
  }));
}

// The continuous wander, px. Octave amps sum to 1, so |offset| ≤ FLOW_MAX.
export function flowOffset(slug: string, wordIndex: number): number {
  return (
    FLOW_MAX *
    flowOctaves(slug).reduce(
      (sum, o) => sum + o.amp * Math.sin((TAU * wordIndex) / o.period + o.phase),
      0,
    )
  );
}

// Quantised to 16 classes (~0.06px steps — far below perception, so the
// curve stays visually continuous while the shipped HTML stays one short
// class per word).
export function flowBucketIndex(slug: string, wordIndex: number): number {
  const t = (flowOffset(slug, wordIndex) + FLOW_MAX) / (2 * FLOW_MAX);
  return Math.max(0, Math.min(FLOW_STEPS - 1, Math.floor(t * FLOW_STEPS)));
}

export function flowClassName(slug: string, wordIndex: number): string {
  return `${FLOW_CLASS_PREFIX}${flowBucketIndex(slug, wordIndex)}`;
}

// Word-gap rhythm: on the reference pages, spacing WITHIN words stays
// disciplined but the gaps BETWEEN words breathe — it's the first discipline
// the hand lets go of in flow. Gap noise is horizontal and between words, so
// (unlike vertical drift) per-word independence cannot produce the
// outlier-off-the-line glitch; a golden-ratio scramble of the flow bucket
// decorrelates gap from drift without spending a second class on it.
function flowGapEm(bucket: number): string {
  return (0.01 + 0.05 * ((bucket * 0.6180339887) % 1)).toFixed(4);
}

// The full class list a word span wears. Single source for both writers, so
// the index excerpt and the letter body can never drift apart.
export function inkSpanClasses(slug: string, wordIndex: number): string {
  return `${inkClassName(slug, wordIndex)} ${flowClassName(slug, wordIndex)}`;
}

// CSS for the ink buckets, scoped to the letters treatment.
// font-variation-settings addresses the wght axis directly, below font-weight
// in the cascade's font-matching, so the buckets ride on top of
// --letters-body-weight without fighting it anywhere else.
export function inkClassRules(scope: string): string {
  return INK_BUCKETS.map(
    (b, i) =>
      `${scope} .${INK_CLASS_PREFIX}${i}{font-variation-settings:'wght' ${b.wght};opacity:${b.opacity};}`,
  ).join("");
}

// CSS for the flow classes. position:relative moves plain inline boxes, so a
// word rides a hair off the ruled line without becoming inline-block (which
// would change line breaking); padding-right widens the gap after the word
// without touching the glyph spacing inside it.
export function flowClassRules(scope: string): string {
  const step = (2 * FLOW_MAX) / FLOW_STEPS;
  return Array.from({ length: FLOW_STEPS }, (_, j) => {
    const top = (-FLOW_MAX + step * (j + 0.5)).toFixed(3);
    return `${scope} .${FLOW_CLASS_PREFIX}${j}{position:relative;top:${top}px;padding-right:${flowGapEm(j)}em;}`;
  }).join("");
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
        (word) => `<span class="${inkSpanClasses(slug, wordIndex++)}">${word}</span>`,
      );
    })
    .join("");
}

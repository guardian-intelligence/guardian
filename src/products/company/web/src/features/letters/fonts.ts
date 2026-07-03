// Single source of truth for Letters typography.
//
// Change the reading face here and it propagates to everything that needs it:
//   - @font-face rules + font variables → lettersTypographyCss(), inlined into
//     the letters critical CSS by routes/letters/route.tsx
//   - the OG card family + baked bytes   → og/template.ts, vite `og-fonts`
//     plugin, og/raster.ts
//   - the dispatch signature             → routes/letters/$slug.tsx
//
// woff2 files live in public/fonts (self-hosted; CSP is font-src 'self').
//
// Per-word ink variation (the gel-pen hand) lives in ink.ts; its class rules
// are appended here so first paint already has the ink.

import { flowClassRules, inkClassRules } from "~/features/letters/ink";

export interface LetterFontFace {
  readonly file: string;
  readonly style: "normal" | "italic";
  // CSS font-weight descriptor: a range like "400 700" for a variable face, or
  // a single value like "500" for a static instance.
  readonly weight: string;
  readonly variable: boolean;
}

export interface LettersFont {
  readonly family: string;
  readonly stack: string;
  readonly weight: number; // body copy weight
  readonly bodySize: string; // body copy font-size (a CSS length / clamp)
  readonly linePitch: number; // px between baselines; the ruling derives from it
  readonly ogFile: string; // upright face baked into the OG rasteriser
  readonly faces: readonly LetterFontFace[];
}

// The reading face. Crimson Pro runs small on the body, so it takes a larger
// measure than a typical text face would.
//
// linePitch is the sheet's registration constant: on the original pages the
// hand writes on every other cell of the graph — x-height filling the lower
// cell, one quiet cell above, baseline on the rule. So the minor cell is
// half the pitch, the major rule is five pitches, and body line-height IS
// the pitch: the ruling and the writing are one system, not a background
// behind a foreground. Grid vars emitted below; consumed by
// .letters-paper-grid in app.css and the paragraph rhythm in typography.tsx.
export const lettersBodyFont: LettersFont = {
  family: "Crimson Pro",
  stack: "'Crimson Pro', Georgia, serif",
  weight: 500,
  bodySize: "clamp(20px, 1.5vw, 22px)",
  linePitch: 32,
  ogFile: "CrimsonPro-OG.ttf",
  faces: [
    { file: "CrimsonPro-Variable.woff2", style: "normal", weight: "400 700", variable: true },
    {
      file: "CrimsonPro-Italic-Variable.woff2",
      style: "italic",
      weight: "400 700",
      variable: true,
    },
  ],
};

// The date hand. The dispatch sign-off itself is the traced SVG in
// handwritten-signature.tsx; this face stays for the handwritten date.
export const lettersSignatureFont = {
  family: "Pinyon Script",
  stack: "'Pinyon Script', cursive",
  faces: [
    { file: "PinyonScript-Regular.woff2", style: "normal", weight: "400", variable: false },
  ] satisfies readonly LetterFontFace[],
} as const;

function faceCss(family: string, f: LetterFontFace): string {
  const format = f.variable ? "woff2-variations" : "woff2";
  return `@font-face{font-family:"${family}";src:url("/fonts/${f.file}") format("${format}");font-weight:${f.weight};font-style:${f.style};font-display:swap;}`;
}

// CSS for the letters treatment: @font-face rules, the font variables the shell
// + typography read, and the body size/weight. Concatenated into the inlined
// critical CSS so first paint already has the face.
export function lettersTypographyCss(): string {
  const faces = [
    ...lettersBodyFont.faces.map((f) => faceCss(lettersBodyFont.family, f)),
    ...lettersSignatureFont.faces.map((f) => faceCss(lettersSignatureFont.family, f)),
  ].join("");
  const vars =
    `[data-treatment="letters"]{` +
    `--font-display:${lettersBodyFont.stack};` +
    `--treatment-display-font:${lettersBodyFont.stack};` +
    `--treatment-body-font:${lettersBodyFont.stack};` +
    `--letters-body-weight:${lettersBodyFont.weight};` +
    `--letters-body-size:${lettersBodyFont.bodySize};` +
    `--letters-line-pitch:${lettersBodyFont.linePitch}px;` +
    `--letters-grid-minor:${lettersBodyFont.linePitch / 2}px;` +
    `--letters-grid-major:${lettersBodyFont.linePitch * 5}px;` +
    // Ink on paper, not pixels on glass: grayscale antialiasing (the same
    // rasterisation design tools force) instead of subpixel RGB fringing,
    // real kerning/ligatures, and no synthesised faces — the variable file
    // carries every weight the treatment asks for.
    `-webkit-font-smoothing:antialiased;` +
    `-moz-osx-font-smoothing:grayscale;` +
    `text-rendering:optimizeLegibility;` +
    `font-kerning:normal;` +
    `font-synthesis:none;}`;
  // Body copy AND the index preview carry the configured size + weight — the
  // preview is the same sheet the letter opens into, so it must never render
  // a weight thinner than the body it becomes (it did: this rule used to give
  // [data-letter-slot="body"] only the size, and the Tailwind font-normal
  // underneath dropped the excerpt to 400 against the body's 500). The
  // salutation is written by the same hand. Outranks the prose utilities
  // (unlayered beats Tailwind layers).
  const body =
    `[data-treatment="letters"] [data-letter-body] p,` +
    `[data-treatment="letters"] [data-letter-body] li,` +
    `[data-treatment="letters"] [data-letter-body] blockquote` +
    `{font-weight:var(--letters-body-weight);font-size:var(--letters-body-size);line-height:var(--letters-line-pitch);}` +
    `[data-treatment="letters"] [data-letter-slot="body"]` +
    `{font-weight:var(--letters-body-weight);font-size:var(--letters-body-size);line-height:var(--letters-line-pitch);}` +
    `[data-treatment="letters"] [data-letter-slot="salutation"]` +
    `{font-weight:var(--letters-body-weight);}`;
  // The hand's paint-time distortion: an SVG displacement field defined by
  // routes/letters/route.tsx (#letters-hand-filter). Long-wavelength vertical
  // displacement leans each word a degree or two while it stays anchored to
  // the ruling; a fine second stage roughens glyph edges like ink wicking
  // into fibre. Paint-only: the DOM stays selectable, findable text.
  const hand =
    `[data-treatment="letters"] [data-letter-slot="body"],` +
    `[data-treatment="letters"] [data-letter-slot="salutation"]` +
    `{filter:url(#letters-hand-filter);}`;
  // The gel bloom: a zero-offset hairline shadow in the ink's own colour.
  // Browsers antialias each glyph in isolation against the alpha ramp gamma
  // gives them; design tools (Figma) rasterise with gamma-aware blending, so
  // their stems hold weight and their edges finish soft instead of thin.
  // Filling the outer half of that alpha ramp with 38% ink is the closest CSS
  // gets: edges read soft, stems read inked, nothing reads blurred. Inherited
  // text-shadow, so the ink spans (and their opacity) carry it for free.
  // text-shadow is the third knob next to ink.ts INK_BUCKETS: blur radius =
  // bloom size (device px at 2x), colour percentage = bloom strength.
  const bloom =
    `[data-treatment="letters"] [data-letter-body],` +
    `[data-treatment="letters"] [data-letter-slot="body"],` +
    `[data-treatment="letters"] [data-letter-slot="salutation"]` +
    `{text-shadow:0 0 0.5px color-mix(in oklab,currentColor 38%,transparent);}`;
  return (
    faces +
    vars +
    body +
    hand +
    bloom +
    inkClassRules('[data-treatment="letters"]') +
    flowClassRules('[data-treatment="letters"]')
  );
}

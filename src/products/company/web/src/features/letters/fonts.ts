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

import { flowClassRules, inkClassRules, tiltClassRules } from "~/features/letters/ink";

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
  readonly bodySize: string; // body copy font-size (derived from the square)
  readonly linePitch: number; // px between baselines; two graph squares
  readonly ogFile: string; // upright face baked into the OG rasteriser
  readonly faces: readonly LetterFontFace[];
}

// --- The sheet's pattern ----------------------------------------------------
//
// The pattern of the original pages: capitals reach one full graph square,
// g and j sink a little below it, one square between lines, baseline on
// every other rule. But caps are not what the eye reads as size — measured
// off the pages, the LOWERCASE bodies (the mass of any text) fill ~0.55 of
// a square, while Crimson Pro's x-height is 0.73 of its caps (430 vs 587).
// Anchoring caps to the square was tried and inflated the lowercase a third
// past the pages — and the set crowded, a serif at weight 500 laying down
// roughly twice the ink of a fine gel pen. So the translation anchors the
// x-height instead:
//
//   square     — the one aesthetic free variable (mobile takes a smaller one)
//   body size  = square × 0.55 / x-height ratio  (lowercase sits in the square
//                the way the hand's did; caps overshoot to ~¾ square — the
//                deliberate concession to type's heavier ink)
//   line pitch = 2 squares                       (baseline on every other rule)
//
// Vertical metrics are unambiguous (typo ascender 918 / descender 220 over
// upm 1024, hhea agrees, USE_TYPO_METRICS set), so every modern browser
// builds the same line box and the registration below is exact.
const CRIMSON_XHEIGHT = 430 / 1024; // measured from the x outline; OS/2 agrees
const HAND_XHEIGHT_FILL = 0.55; // lowercase fill of one square, off the pages
const CRIMSON_ASCENT = 918 / 1024;
const CRIMSON_CONTENT = (918 + 220) / 1024;

const GRID_SQUARE_PX = 17;
const GRID_SQUARE_MOBILE_PX = 14; // below the 40rem (sm) breakpoint

// The letter page's masthead above the ruled stack, top of sheet → top of the
// date box — all viewport-independent constants, kept that way on purpose
// (the page top padding is fixed across breakpoints for exactly this reason):
// chrome header 45px (edge gap 10 + lockup 22 + rule gap 13, see letters.css)
// plus page top padding 24px. Below it the masthead advances in whole
// pitches: date box two, salutation margin + box + body margin one each.
const MASTHEAD_FIXED_PX = 45 + 24;
const MASTHEAD_PITCHES = 5;

interface SheetScale {
  readonly square: number;
  readonly pitch: number;
  readonly bodySize: number;
  // Vertical offset that slides the ruling into registration: the body's
  // first baseline sits ON a rule, statically — no measuring JS, no flash.
  readonly phase: number;
}

function sheetScale(square: number): SheetScale {
  const pitch = square * 2;
  const bodySize = (square * HAND_XHEIGHT_FILL) / CRIMSON_XHEIGHT;
  // First-baseline offset inside a line box one pitch tall.
  const baselineInBox = (pitch - bodySize * CRIMSON_CONTENT) / 2 + bodySize * CRIMSON_ASCENT;
  const firstBaseline = MASTHEAD_FIXED_PX + MASTHEAD_PITCHES * pitch + baselineInBox;
  return { square, pitch, bodySize, phase: firstBaseline % pitch };
}

const SHEET = sheetScale(GRID_SQUARE_PX);
const SHEET_MOBILE = sheetScale(GRID_SQUARE_MOBILE_PX);

function sheetScaleVars(s: SheetScale): string {
  return (
    `--letters-body-size:${s.bodySize.toFixed(2)}px;` +
    `--letters-line-pitch:${s.pitch}px;` +
    `--letters-grid-minor:${s.square}px;` +
    `--letters-grid-major:${s.pitch * 5}px;`
  );
}

// The reading face. Sized by the square (above), not by taste per breakpoint.
export const lettersBodyFont: LettersFont = {
  family: "Crimson Pro",
  stack: "'Crimson Pro', Georgia, serif",
  weight: 500,
  bodySize: `${SHEET.bodySize.toFixed(2)}px`,
  linePitch: SHEET.pitch,
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
// critical CSS so first paint already has the face — the ruling is layout, and
// it must never flash into place.
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
    sheetScaleVars(SHEET) +
    // Ink on paper, not pixels on glass: grayscale antialiasing (the same
    // rasterisation design tools force) instead of subpixel RGB fringing,
    // real kerning/ligatures, and no synthesised faces — the variable file
    // carries every weight the treatment asks for.
    `-webkit-font-smoothing:antialiased;` +
    `-moz-osx-font-smoothing:grayscale;` +
    `text-rendering:optimizeLegibility;` +
    `font-kerning:normal;` +
    `font-synthesis:none;}`;
  // Registration: on the letter page (route.tsx marks it data-letters-page)
  // the ruling slides by the computed phase so the body's first baseline sits
  // ON a rule. Mobile takes the smaller square, and its own phase with it.
  const registration =
    `[data-treatment="letters"][data-letters-page="detail"]{--letters-grid-phase-y:${SHEET.phase.toFixed(2)}px;}` +
    `@media (width < 40rem){` +
    `[data-treatment="letters"]{${sheetScaleVars(SHEET_MOBILE)}}` +
    `[data-treatment="letters"][data-letters-page="detail"]{--letters-grid-phase-y:${SHEET_MOBILE.phase.toFixed(2)}px;}` +
    `}`;
  // Body copy AND the index preview carry the configured size + weight — the
  // preview is the same sheet the letter opens into, so it must never render
  // a weight thinner than the body it becomes (it did: this rule used to give
  // [data-letter-slot="body"] only the size, and the Tailwind font-normal
  // underneath dropped the excerpt to 400 against the body's 500). The
  // salutation is written by the same hand. Line-height IS the ruled pitch.
  // Outranks the prose utilities (unlayered beats Tailwind layers).
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
    registration +
    body +
    hand +
    bloom +
    inkClassRules('[data-treatment="letters"]') +
    flowClassRules('[data-treatment="letters"]') +
    tiltClassRules('[data-treatment="letters"]')
  );
}

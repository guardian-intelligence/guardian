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

import { inkClassRules } from "~/features/letters/ink";

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
  readonly ogFile: string; // upright face baked into the OG rasteriser
  readonly faces: readonly LetterFontFace[];
}

// The reading face. Crimson Pro runs small on the body, so it takes a larger
// measure than a typical text face would.
export const lettersBodyFont: LettersFont = {
  family: "Crimson Pro",
  stack: "'Crimson Pro', Georgia, serif",
  weight: 500,
  bodySize: "clamp(20px, 1.5vw, 22px)",
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
    `{font-weight:var(--letters-body-weight);font-size:var(--letters-body-size);}` +
    `[data-treatment="letters"] [data-letter-slot="body"]` +
    `{font-weight:var(--letters-body-weight);font-size:var(--letters-body-size);}` +
    `[data-treatment="letters"] [data-letter-slot="salutation"]` +
    `{font-weight:var(--letters-body-weight);}`;
  return faces + vars + body + inkClassRules('[data-treatment="letters"]');
}

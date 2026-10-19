// The families /design can set: anveio.com's own, then the Google Fonts
// catalog (snapshotted by scripts/google-fonts-catalog.mjs).
import catalogText from "./google-fonts.json?raw";
import {
  CATEGORIES,
  LOCAL_FONTS,
  type Category,
  googleFamilyParam,
  localFont,
  type Face,
  type FontInfo,
  type GoogleFont,
  type TypeScale,
} from "~/home/typeScale";

function parseCatalog(v: unknown): FontInfo[] {
  if (!Array.isArray(v)) return [];
  const weights = (w: unknown) =>
    Array.isArray(w) ? w.filter((n): n is number => typeof n === "number") : [];
  const local = new Set(LOCAL_FONTS.map((f) => f.family));
  const out: FontInfo[] = [];
  for (const row of v) {
    if (!Array.isArray(row)) continue;
    const [family, cat, roman, italic, axes] = row;
    const category = typeof cat === "number" ? CATEGORIES[cat] : undefined;
    if (typeof family !== "string" || !category || local.has(family)) continue;
    const ranges: Record<string, readonly [number, number]> = {};
    for (const a of Array.isArray(axes) ? axes : []) {
      if (Array.isArray(a) && typeof a[0] === "string" && a[0] !== "ital") {
        const [lo, hi] = [a[1], a[2]];
        if (typeof lo === "number" && typeof hi === "number") ranges[a[0]] = [lo, hi];
      }
    }
    out.push({ family, category, roman: weights(roman), italic: weights(italic), axes: ranges });
  }
  return out;
}

export const CATALOG: readonly FontInfo[] = parseCatalog(JSON.parse(catalogText));
const byFamily = new Map(CATALOG.map((f) => [f.family, f]));

export function fontInfo(family: string, scale: TypeScale): FontInfo | undefined {
  const google = scale.google[family];
  return localFont(family) ?? (google ? { family, ...google } : byFamily.get(family));
}

export const isLocal = (family: string) => localFont(family) !== undefined;

/** The scale with its Google table matching the families its styles use. */
export function withGoogle(scale: TypeScale): TypeScale {
  const google: Record<string, GoogleFont> = {};
  for (const s of Object.values(scale.styles)) {
    for (const family of [s.desktop.family, s.phone.family]) {
      if (!family || isLocal(family)) continue;
      const font = scale.google[family] ?? byFamily.get(family);
      if (font) {
        const { category, roman, italic, axes } = font;
        google[family] = { category, roman, italic, axes };
      }
    }
  }
  return { ...scale, google };
}

const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));

/** The closest face `font` has to `face`: weight, italic and axes it supports. */
export function withFamily(
  face: Face,
  font: FontInfo,
): Pick<Face, "family" | "weight" | "italic" | "axes"> {
  const italic = font.italic.length === 0 ? false : font.roman.length === 0 ? true : face.italic;
  const weights = italic ? font.italic : font.roman;
  const range = font.axes["wght"];
  const weight = range
    ? clamp(face.weight, range[0], range[1])
    : weights.reduce(
        (best, w) => (Math.abs(w - face.weight) < Math.abs(best - face.weight) ? w : best),
        weights[0] ?? 400,
      );
  const axes: Record<string, number> = {};
  for (const [tag, v] of Object.entries(face.axes)) {
    const r = font.axes[tag];
    if (r) axes[tag] = clamp(v, r[0], r[1]);
  }
  return { family: font.family, weight, italic, axes };
}

export const WEIGHT_NAMES: Readonly<Record<number, string>> = {
  100: "Thin",
  200: "ExtraLight",
  300: "Light",
  400: "Regular",
  500: "Medium",
  600: "SemiBold",
  700: "Bold",
  800: "ExtraBold",
  900: "Black",
  1000: "ExtraBlack",
};

export const AXIS_NAMES: Readonly<Record<string, string>> = {
  wdth: "Width",
  opsz: "Optical size",
  slnt: "Slant",
  GRAD: "Grade",
  SOFT: "Softness",
  WONK: "Wonky",
  CASL: "Casual",
  CRSV: "Cursive",
  MONO: "Monospace",
  XHGT: "x-height",
  YTUC: "Cap height",
  ROND: "Roundness",
};

const CATEGORY_NAMES = {
  sans: "Sans",
  serif: "Serif",
  display: "Display",
  handwriting: "Script",
  mono: "Mono",
} as const;
export const categoryName = (c: Category) => CATEGORY_NAMES[c];

// Font-name previews in the editor's own chrome load only the glyphs of
// the name (css2's text=), so a long list costs a few KB.
const previewed = new Set<string>();
export function loadPreview(font: FontInfo) {
  if (isLocal(font.family) || previewed.has(font.family)) return;
  previewed.add(font.family);
  const link = document.createElement("link");
  link.rel = "stylesheet";
  link.href = `https://fonts.googleapis.com/css2?family=${googleFamilyParam(font.family, font)}&text=${encodeURIComponent(font.family)}`;
  document.head.append(link);
}

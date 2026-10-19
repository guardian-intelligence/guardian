/* The home page's type scale: the face, weight and size of every kind of
   text, on desktop and on phones. The values live in type-scale.json, which
   the /design editor rewrites; this module validates them and turns them
   into the page's CSS. It imports nothing, so vite.config.ts can validate a
   save without watching the JSON it writes. */

export const PHONE_MAX = 860;

export const CATEGORIES = ["sans", "serif", "display", "handwriting", "mono"] as const;
export type Category = (typeof CATEGORIES)[number];

export const CASES = ["none", "uppercase", "lowercase", "capitalize"] as const;
export type TextCase = (typeof CASES)[number];

/** Variation axes by tag, wght among them when the family is variable. */
export type Axes = Readonly<Record<string, readonly [min: number, max: number]>>;

/** What a family offers: its upright and italic weights, and its axes. */
export type FontInfo = {
  readonly family: string;
  readonly category: Category;
  readonly roman: readonly number[];
  readonly italic: readonly number[];
  readonly axes: Axes;
};
export type GoogleFont = Omit<FontInfo, "family">;

export type Face = {
  family: string;
  weight: number;
  italic: boolean;
  /** px */
  size: number;
  /** px */
  lineHeight: number;
  /** em */
  tracking: number;
  case: TextCase;
  /** Settings for axes other than wght; an absent axis keeps its default. */
  axes: Readonly<Record<string, number>>;
};

/** Phones inherit every desktop value they don't override. */
export type TypeStyle = { readonly desktop: Face; readonly phone: Partial<Face> };

export const STYLES = [
  { id: "lockup", label: "Lockup", group: "Header", selector: ".hp-lockup" },
  { id: "navLink", label: "Nav links", group: "Header", selector: ".hp-nav-links a, .hp-nav-link" },
  { id: "button", label: "Buttons", group: "Header", selector: ".hp-btn" },
  { id: "soon", label: "Coming soon tag", group: "Header", selector: ".hp-soon" },
  { id: "title", label: "Headline", group: "Hero", selector: ".hp-title" },
  { id: "sub", label: "Subtitle", group: "Hero", selector: ".hp-sub" },
  { id: "bandTitle", label: "Band heading", group: "Band", selector: ".hp-infra h2" },
  { id: "bandBody", label: "Band text", group: "Band", selector: ".hp-infra p" },
  { id: "cellTitle", label: "Card title", group: "Data", selector: ".hp-cell h3" },
  { id: "cellBody", label: "Card text", group: "Data", selector: ".hp-cell p" },
  { id: "sectionTitle", label: "Section heading", group: "Sections", selector: ".hp-serif" },
  { id: "sectionLede", label: "Section lede", group: "Sections", selector: ".hp-section-head p" },
  { id: "badge", label: "Badge numeral", group: "Sections", selector: ".hp-badge" },
  { id: "whyTitle", label: "Reason title", group: "Sections", selector: ".hp-why-cell h3" },
  { id: "whyBody", label: "Reason text", group: "Sections", selector: ".hp-why-cell p" },
  { id: "faqQ", label: "FAQ question", group: "Sections", selector: ".hp-faq summary" },
  { id: "faqA", label: "FAQ answer", group: "Sections", selector: ".hp-faq details p" },
  { id: "footTitle", label: "Footer heading", group: "Footer", selector: ".hp-foot-cta h2" },
  { id: "footLede", label: "Footer lede", group: "Footer", selector: ".hp-foot-cta p" },
  { id: "footBrand", label: "Footer tagline", group: "Footer", selector: ".hp-foot-brand p" },
  { id: "footHead", label: "Column heading", group: "Footer", selector: ".hp-foot-col h4" },
  { id: "footLink", label: "Footer links", group: "Footer", selector: ".hp-foot-col a" },
  { id: "footSmall", label: "Small print", group: "Footer", selector: ".hp-foot-copy" },
] as const;
export type StyleId = (typeof STYLES)[number]["id"];

/** Above the fold: their local font files are preloaded. */
const FIRST_PAINT: readonly StyleId[] = ["lockup", "navLink", "button", "title", "sub"];

export type TypeScale = {
  readonly styles: Readonly<Record<StyleId, TypeStyle>>;
  /** The Google Fonts families the styles use, as the catalog described them. */
  readonly google: Readonly<Record<string, GoogleFont>>;
};

type LocalFace = { file: string; weight: readonly [number, number]; italic: boolean };
type LocalFont = FontInfo & {
  fallback: string;
  faces: readonly LocalFace[];
  /** Axis settings the family always carries unless a style overrides them. */
  pinnedAxes?: Readonly<Record<string, number>>;
  /** Declared in app.css for every page; the rest are declared here when used. */
  inApp: boolean;
};

const stops = (min: number, max: number) =>
  [100, 200, 300, 400, 500, 600, 700, 800, 900].filter((w) => w >= min && w <= max);

/** The faces anveio.com serves itself, from /deck/fonts. */
export const LOCAL_FONTS: readonly LocalFont[] = [
  {
    family: "Geist",
    category: "sans",
    fallback: "ui-sans-serif, system-ui, sans-serif",
    roman: stops(100, 900),
    italic: stops(100, 900),
    axes: { wght: [100, 900] },
    faces: [
      { file: "Geist-Variable.woff2", weight: [100, 900], italic: false },
      { file: "Geist-Italic-Variable.woff2", weight: [100, 900], italic: true },
    ],
    inApp: true,
  },
  {
    family: "Archivo",
    category: "sans",
    fallback: '"Geist", ui-sans-serif, system-ui, sans-serif',
    roman: stops(100, 900),
    italic: [],
    axes: { wdth: [62, 125], wght: [100, 900] },
    faces: [{ file: "Archivo-Variable.woff2", weight: [100, 900], italic: false }],
    inApp: true,
  },
  {
    family: "Roboto",
    category: "sans",
    fallback: "ui-sans-serif, system-ui, sans-serif",
    roman: stops(100, 900),
    italic: stops(100, 900),
    axes: { wdth: [75, 100], wght: [100, 900] },
    faces: [
      { file: "Roboto-Variable.woff2", weight: [100, 900], italic: false },
      { file: "Roboto-Italic-Variable.woff2", weight: [100, 900], italic: true },
    ],
    inApp: false,
  },
  {
    family: "Libre Franklin",
    category: "sans",
    fallback: "ui-sans-serif, system-ui, sans-serif",
    roman: stops(100, 900),
    italic: stops(100, 900),
    axes: { wght: [100, 900] },
    faces: [
      { file: "LibreFranklin-Variable.woff2", weight: [100, 900], italic: false },
      { file: "LibreFranklin-Italic-Variable.woff2", weight: [100, 900], italic: true },
    ],
    inApp: false,
  },
  {
    family: "Source Sans 3",
    category: "sans",
    fallback: "ui-sans-serif, system-ui, sans-serif",
    roman: stops(200, 900),
    italic: stops(200, 900),
    axes: { wght: [200, 900] },
    faces: [
      { file: "SourceSans3-Variable.woff2", weight: [200, 900], italic: false },
      { file: "SourceSans3-Italic-Variable.woff2", weight: [200, 900], italic: true },
    ],
    inApp: false,
  },
  {
    family: "Source Serif 4 Display",
    category: "serif",
    fallback: "Georgia, serif",
    // Adobe's static Display cut (~85 KB a weight) covers 200, 300, 400,
    // 600, 700 and 900. The weights between come from the variable Source
    // Serif 4 at opsz 60, the same design, declared only for the gaps:
    // browsers disagree on which face wins where two match (Chromium takes
    // the variable one), and the static file is a fifth the size.
    roman: stops(200, 900),
    italic: stops(200, 900),
    axes: { wght: [200, 900] },
    pinnedAxes: { opsz: 60 },
    faces: [
      ...(
        [
          [201, 299],
          [301, 399],
          [401, 599],
          [601, 699],
          [701, 899],
        ] as const
      ).flatMap((weight) => [
        { file: "SourceSerif4Variable-Roman.woff2", weight, italic: false },
        { file: "SourceSerif4Variable-Italic.woff2", weight, italic: true },
      ]),
      ...(
        [
          [200, "ExtraLight"],
          [300, "Light"],
          [400, "Regular"],
          [600, "Semibold"],
          [700, "Bold"],
          [900, "Black"],
        ] as const
      ).flatMap(([w, name]) => [
        { file: `SourceSerif4Display-${name}.woff2`, weight: [w, w] as const, italic: false },
        {
          file: `SourceSerif4Display-${name === "Regular" ? "" : name}It.woff2`,
          weight: [w, w] as const,
          italic: true,
        },
      ]),
    ],
    inApp: false,
  },
  {
    family: "Source Serif 4",
    category: "serif",
    fallback: "Georgia, serif",
    roman: stops(200, 900),
    italic: stops(200, 900),
    axes: { opsz: [8, 60], wght: [200, 900] },
    faces: [
      { file: "SourceSerif4Variable-Roman.woff2", weight: [200, 900], italic: false },
      { file: "SourceSerif4Variable-Italic.woff2", weight: [200, 900], italic: true },
    ],
    inApp: false,
  },
  {
    family: "Instrument Serif",
    category: "serif",
    fallback: "Georgia, serif",
    roman: [],
    italic: [400],
    axes: {},
    faces: [{ file: "InstrumentSerif-Italic.woff2", weight: [400, 400], italic: true }],
    inApp: true,
  },
  {
    family: "Noto Serif Display",
    category: "serif",
    fallback: "Georgia, serif",
    roman: [600],
    italic: [],
    axes: {},
    faces: [
      { file: "NotoSerifDisplay-ExtraCondensedSemiBold.woff2", weight: [600, 600], italic: false },
    ],
    inApp: true,
  },
];

const FALLBACK: Readonly<Record<Category, string>> = {
  sans: "ui-sans-serif, system-ui, sans-serif",
  serif: "Georgia, serif",
  display: "ui-sans-serif, system-ui, sans-serif",
  handwriting: "cursive",
  mono: "ui-monospace, monospace",
};

export const localFont = (family: string) => LOCAL_FONTS.find((f) => f.family === family);

export const phoneFace = (style: TypeStyle): Face => ({ ...style.desktop, ...style.phone });

export function usedFamilies(scale: TypeScale): Set<string> {
  const used = new Set<string>();
  for (const s of STYLES) {
    const style = scale.styles[s.id];
    used.add(style.desktop.family);
    if (style.phone.family) used.add(style.phone.family);
  }
  return used;
}

/** Trims float noise: 62.720000001 → 62.72. */
export const num = (v: number) => String(Math.round(v * 1000) / 1000);

function stack(family: string, scale: TypeScale) {
  const fallback =
    localFont(family)?.fallback ?? FALLBACK[scale.google[family]?.category ?? "sans"];
  return `"${family}", ${fallback}`;
}

function declarations(face: Face, scale: TypeScale) {
  const axes = { ...localFont(face.family)?.pinnedAxes, ...face.axes };
  const settings = Object.entries(axes).map(([tag, v]) => `"${tag}" ${num(v)}`);
  const wdth = face.axes["wdth"];
  return [
    `font-family: ${stack(face.family, scale)}`,
    `font-weight: ${num(face.weight)}`,
    `font-style: ${face.italic ? "italic" : "normal"}`,
    `font-stretch: ${wdth === undefined ? "normal" : `${num(wdth)}%`}`,
    `font-size: ${num(face.size)}px`,
    `line-height: ${num(face.lineHeight)}px`,
    `letter-spacing: ${face.tracking === 0 ? "normal" : `${num(face.tracking)}em`}`,
    `text-transform: ${face.case}`,
    `font-variation-settings: ${settings.length ? settings.join(", ") : "normal"}`,
    // For trimming the tracking after a line's last letter (home.css).
    `--hp-track: ${num(face.tracking)}em`,
  ].join("; ");
}

function faceCss(family: string, face: LocalFace) {
  const [lo, hi] = face.weight;
  return (
    `@font-face { font-family: "${family}"; src: url("/deck/fonts/${face.file}") format("woff2"); ` +
    `font-weight: ${lo === hi ? lo : `${lo} ${hi}`}; font-style: ${face.italic ? "italic" : "normal"}; ` +
    "font-display: swap; }"
  );
}

/** @font-face rules for every face anveio.com serves that app.css doesn't declare. */
export const localFaceCss = (families?: Set<string>) =>
  LOCAL_FONTS.filter((f) => !f.inApp && (!families || families.has(f.family)))
    .flatMap((f) => f.faces.map((face) => faceCss(f.family, face)))
    .join("\n");

/** The page's type CSS. Rules are `.hp :is(…)`, so they outrank home.css. */
export function typeCss(scale: TypeScale): string {
  const rule = (selector: string, face: Face) =>
    `.hp :is(${selector}) { ${declarations(face, scale)}; }`;
  const phone = STYLES.filter((s) => Object.keys(scale.styles[s.id].phone).length > 0);
  return [
    localFaceCss(usedFamilies(scale)),
    ...STYLES.map((s) => rule(s.selector, scale.styles[s.id].desktop)),
    `@media (max-width: ${PHONE_MAX}px) {`,
    ...phone.map((s) => `  ${rule(s.selector, phoneFace(scale.styles[s.id]))}`),
    "}",
  ]
    .filter(Boolean)
    .join("\n");
}

/** CSS font shorthands for the faces the first paint uses, for document.fonts.load. */
export function firstPaintFonts(scale: TypeScale): string[] {
  const fonts = new Set<string>();
  for (const id of FIRST_PAINT) {
    for (const f of [scale.styles[id].desktop, phoneFace(scale.styles[id])]) {
      fonts.add(`${f.italic ? "italic " : ""}${num(f.weight)} ${num(f.size)}px "${f.family}"`);
    }
  }
  return [...fonts];
}

/** Local font files the first paint needs, other than Geist upright (root preloads it). */
export function preloadFiles(scale: TypeScale): string[] {
  const files = new Set<string>();
  for (const id of FIRST_PAINT) {
    for (const face of [scale.styles[id].desktop, phoneFace(scale.styles[id])]) {
      // An exact static face beats a variable one, as in the page's own matching.
      const faces = (localFont(face.family)?.faces ?? []).filter(
        (f) => f.italic === face.italic && face.weight >= f.weight[0] && face.weight <= f.weight[1],
      );
      const match = faces.find((f) => f.weight[0] === f.weight[1]) ?? faces[0];
      if (match && match.file !== "Geist-Variable.woff2") files.add(match.file);
    }
  }
  return [...files];
}

// Google's css2 API wants axis tags lowercase first, each group alphabetical.
const axisOrder = (a: string, b: string) => {
  const upper = (t: string) => (t[0] === t[0]?.toUpperCase() ? 1 : 0);
  return upper(a) - upper(b) || (a < b ? -1 : a > b ? 1 : 0);
};

/** One css2 `family=` value loading everything the family has. */
export function googleFamilyParam(family: string, font: GoogleFont): string {
  const name = encodeURIComponent(family).replaceAll("%20", "+");
  const ital = font.italic.length > 0;
  const range = (tag: string) => {
    const [lo, hi] = font.axes[tag] ?? [0, 0];
    return lo === hi ? num(lo) : `${num(lo)}..${num(hi)}`;
  };
  const variable = "wght" in font.axes;
  const tags = [...Object.keys(font.axes), ...(variable ? [] : ["wght"])].sort(axisOrder);
  const header = [...(ital ? ["ital"] : []), ...tags].join(",");
  const tuple = (italic: 0 | 1, weight?: number) =>
    [
      ...(ital ? [italic] : []),
      ...tags.map((t) => (t === "wght" && weight !== undefined ? weight : range(t))),
    ].join(",");
  const tuples = variable
    ? [tuple(0), ...(ital ? [tuple(1)] : [])]
    : [...font.roman.map((w) => tuple(0, w)), ...font.italic.map((w) => tuple(1, w))];
  if (tags.length === 1 && !ital && font.roman.length === 1 && font.roman[0] === 400) return name;
  return `${name}:${header}@${tuples.join(";")}`;
}

export const googleHref = (families: [string, GoogleFont][]) =>
  `https://fonts.googleapis.com/css2?${families
    .map(([f, font]) => `family=${googleFamilyParam(f, font)}`)
    .join("&")}&display=swap`;

/** The stylesheet for the Google families the scale uses, if any. */
export function googleStylesheet(scale: TypeScale): string | undefined {
  const used = usedFamilies(scale);
  const families = Object.entries(scale.google).filter(([f]) => used.has(f));
  return families.length ? googleHref(families) : undefined;
}

// Validation: the JSON is rewritten by a browser, so nothing is trusted.
const FAMILY = /^[A-Za-z0-9][A-Za-z0-9 -]{0,63}$/;
const TAG = /^[A-Za-z0-9]{4}$/;
export const FACE_KEYS = [
  "family",
  "weight",
  "italic",
  "size",
  "lineHeight",
  "tracking",
  "case",
  "axes",
] as const;

const isRecord = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);

function fail(path: string, what: string): never {
  throw new Error(`type-scale.json: ${path} ${what}`);
}

function number(v: unknown, path: string, min: number, max: number): number {
  if (typeof v !== "number" || !Number.isFinite(v) || v < min || v > max) {
    fail(path, `must be a number from ${min} to ${max}`);
  }
  return v;
}

function numbers(v: unknown, path: string): number[] {
  if (!Array.isArray(v)) fail(path, "must be a list of weights");
  return v.map((w, i) => number(w, `${path}[${i}]`, 1, 1000));
}

function family(v: unknown, path: string): string {
  if (typeof v !== "string" || !FAMILY.test(v)) fail(path, "must be a font family name");
  return v;
}

function textCase(v: unknown, path: string): TextCase {
  const found = CASES.find((c) => c === v);
  if (!found) fail(path, `must be one of ${CASES.join(", ")}`);
  return found;
}

function axisSettings(v: unknown, path: string): Record<string, number> {
  if (!isRecord(v)) fail(path, "must be an object of axis settings");
  const out: Record<string, number> = {};
  for (const [tag, value] of Object.entries(v)) {
    if (!TAG.test(tag) || tag === "wght") fail(`${path}.${tag}`, "isn't a settable axis");
    out[tag] = number(value, `${path}.${tag}`, -1000, 1000);
  }
  return out;
}

function partialFace(v: unknown, path: string): Partial<Face> {
  if (!isRecord(v)) fail(path, "must be an object");
  const out: Partial<Face> = {};
  for (const key of Object.keys(v)) {
    if (!FACE_KEYS.some((k) => k === key)) fail(`${path}.${key}`, "isn't a type property");
  }
  const at = (k: string) => `${path}.${k}`;
  if ("family" in v) out.family = family(v["family"], at("family"));
  if ("weight" in v) out.weight = number(v["weight"], at("weight"), 1, 1000);
  if ("italic" in v) {
    if (typeof v["italic"] !== "boolean") fail(at("italic"), "must be true or false");
    out.italic = v["italic"];
  }
  if ("size" in v) out.size = number(v["size"], at("size"), 1, 400);
  if ("lineHeight" in v) out.lineHeight = number(v["lineHeight"], at("lineHeight"), 1, 800);
  if ("tracking" in v) out.tracking = number(v["tracking"], at("tracking"), -1, 1);
  if ("case" in v) out.case = textCase(v["case"], at("case"));
  if ("axes" in v) out.axes = axisSettings(v["axes"], at("axes"));
  return out;
}

function fullFace(v: unknown, path: string): Face {
  const face = partialFace(v, path);
  const { family: f, weight, italic, size, lineHeight, tracking, case: c, axes } = face;
  if (
    f === undefined ||
    weight === undefined ||
    italic === undefined ||
    size === undefined ||
    lineHeight === undefined ||
    tracking === undefined ||
    c === undefined ||
    axes === undefined
  ) {
    fail(path, `needs all of ${FACE_KEYS.join(", ")}`);
  }
  return { family: f, weight, italic, size, lineHeight, tracking, case: c, axes };
}

function googleFont(v: unknown, path: string): GoogleFont {
  if (!isRecord(v)) fail(path, "must be an object");
  const category = CATEGORIES.find((c) => c === v["category"]);
  if (!category) fail(`${path}.category`, `must be one of ${CATEGORIES.join(", ")}`);
  const axes = v["axes"];
  if (!isRecord(axes)) fail(`${path}.axes`, "must be an object of axis ranges");
  const ranges: Record<string, readonly [number, number]> = {};
  for (const [tag, r] of Object.entries(axes)) {
    if (!TAG.test(tag) || !Array.isArray(r) || r.length !== 2) {
      fail(`${path}.axes.${tag}`, "must be [min, max]");
    }
    ranges[tag] = [
      number(r[0], `${path}.axes.${tag}[0]`, -1000, 1000),
      number(r[1], `${path}.axes.${tag}[1]`, -1000, 1000),
    ];
  }
  return {
    category,
    roman: numbers(v["roman"], `${path}.roman`),
    italic: numbers(v["italic"], `${path}.italic`),
    axes: ranges,
  };
}

export function parseScale(v: unknown): TypeScale {
  if (!isRecord(v) || !isRecord(v["styles"]) || !isRecord(v["google"])) {
    fail("", "must be { styles, google }");
  }
  const styles = v["styles"];
  const entries = STYLES.map((s): [StyleId, TypeStyle] => {
    const style = styles[s.id];
    if (!isRecord(style)) fail(`styles.${s.id}`, "is missing");
    return [
      s.id,
      {
        desktop: fullFace(style["desktop"], `styles.${s.id}.desktop`),
        phone: partialFace(style["phone"], `styles.${s.id}.phone`),
      },
    ];
  });
  const google: Record<string, GoogleFont> = {};
  for (const [f, font] of Object.entries(v["google"])) {
    google[family(f, `google.${f}`)] = googleFont(font, `google.${f}`);
  }
  return { styles: byStyle(entries), google };
}

/** Every StyleId, mapped; `entries` holds each exactly once by construction. */
function byStyle<T>(entries: [StyleId, T][]): Record<StyleId, T> {
  return Object.fromEntries(entries) as Record<StyleId, T>;
}

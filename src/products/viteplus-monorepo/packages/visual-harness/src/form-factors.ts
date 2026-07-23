export interface FormFactor {
  readonly name: string;
  /** CSS pixels; physical capture size is width × deviceScaleFactor. */
  readonly width: number;
  readonly height: number;
  readonly deviceScaleFactor: number;
  readonly userAgent?: string;
  readonly isMobile?: boolean;
  readonly hasTouch?: boolean;
}

const IPHONE_UA =
  "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1";
const IPAD_UA =
  "Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1";

// Widths bracket the shortty-web breakpoints (64rem/58.75rem/40rem =
// 1024/940/640 CSS px) so every distinct responsive layout has coverage.
// 1920×1080 at deviceScaleFactor 2 captures a physical 3840×2160 frame.
export const FORM_FACTORS: readonly FormFactor[] = [
  { name: "4k-desktop", width: 1920, height: 1080, deviceScaleFactor: 2 },
  { name: "standard-desktop", width: 1440, height: 900, deviceScaleFactor: 2 },
  { name: "laptop", width: 1280, height: 800, deviceScaleFactor: 2 },
  {
    name: "tablet",
    width: 820,
    height: 1180,
    deviceScaleFactor: 2,
    userAgent: IPAD_UA,
    hasTouch: true,
  },
  {
    name: "mobile",
    width: 390,
    height: 844,
    deviceScaleFactor: 3,
    userAgent: IPHONE_UA,
    isMobile: true,
    hasTouch: true,
  },
];

export function formFactor(name: string): FormFactor {
  const found = FORM_FACTORS.find((f) => f.name === name);
  if (!found) {
    const names = FORM_FACTORS.map((f) => f.name).join(", ");
    throw new Error(`unknown form factor ${JSON.stringify(name)}; available: ${names}`);
  }
  return found;
}

export function parseFormFactors(spec: string | undefined): readonly FormFactor[] {
  const trimmed = spec?.trim() ?? "";
  if (trimmed === "" || trimmed === "all") return FORM_FACTORS;
  return trimmed.split(",").map((name) => formFactor(name.trim()));
}

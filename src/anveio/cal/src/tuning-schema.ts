import * as v from "valibot";

// Every tunable lever in the page, grouped the way the studio shows them.
// Values are kept in the studio's units; renderers convert at the GPU boundary.

type Range = {
  readonly kind: "range";
  readonly label: string;
  readonly min: number;
  readonly max: number;
  readonly step: number;
};
type Color = { readonly kind: "color"; readonly label: string };
type Toggle = { readonly kind: "toggle"; readonly label: string };
export type Control = Range | Color | Toggle;

const range = (label: string, min: number, max: number, step: number): Range => ({
  kind: "range",
  label,
  min,
  max,
  step,
});
const color = (label: string): Color => ({ kind: "color", label });
const toggle = (label: string): Toggle => ({ kind: "toggle", label });

export const CONTROLS = {
  orb: {
    accent: color("Deep"),
    soft: color("Light"),
    blush: color("Blush"),
    size: range("Size in bar", 28, 52, 1),
    energyIdle: range("Waves · resting", 0, 1.5, 0.01),
    energyListen: range("Waves · listening", 0, 1.5, 0.01),
    energyThink: range("Waves · working", 0, 1.5, 0.01),
    energySpeak: range("Waves · speaking", 0, 1.5, 0.01),
    rippleSpeed: range("Wave speed", 0.1, 3, 0.01),
    rippleDensity: range("Wave count", 1, 16, 0.1),
    swirl: range("Working swirl", 0, 3, 0.01),
    drift: range("Colour drift", 0, 3, 0.01),
    breatheHz: range("Breath (Hz)", 0.05, 1, 0.01),
    gloss: range("Gloss", 0, 2, 0.01),
    matte: range("Matte (frosted)", 0, 1, 0.01),
    muteDrain: range("Muted grey", 0, 1, 0.01),
  },
  glass: {
    tint: color("Tint"),
    tintAlpha: range("Tint strength", 0, 1, 0.01),
    refThickness: range("Bezel width", 1, 80, 0.1),
    refFactor: range("Refractive index", 1, 4, 0.01),
    refDistance: range("Refraction distance", 0, 0.2, 0.001),
    refDispersion: range("Dispersion", 0, 50, 0.1),
    refFresnelRange: range("Fresnel range", 0, 100, 0.1),
    refFresnelHardness: range("Fresnel hardness", 0, 100, 0.1),
    refFresnelFactor: range("Fresnel strength", 0, 100, 0.1),
    glareRange: range("Glare range", 0, 120, 0.1),
    glareHardness: range("Glare hardness", 0, 100, 0.1),
    glareFactor: range("Glare strength", 0, 120, 0.1),
    glareConvergence: range("Glare convergence", 0, 100, 0.1),
    glareOppositeFactor: range("Opposite glare", 0, 100, 0.1),
    glareAngle: range("Glare angle", -180, 180, 1),
    blurRadius: range("Backdrop blur", 1, 40, 1),
    blurEdge: toggle("Blur under bezel"),
    roundness: range("Corner squircle", 2, 7, 0.1),
    mergeRate: range("Liquid merge", 0, 0.1, 0.001),
    shadowExpand: range("Shadow spread", 0, 100, 0.1),
    shadowFactor: range("Shadow strength", 0, 100, 0.1),
    cover: range("Hide page under glass", 0, 1, 0.01),
    domBlur: range("Page blur under glass", 0, 40, 1),
  },
  ink: {
    tint: color("Slope sheen"),
    swell: range("Swell", 0, 2, 0.01),
    rippleSpeed: range("Ripple speed", 40, 600, 1),
    lifetime: range("Ripple life (s)", 1, 10, 0.1),
    gloss: range("Gloss", 0, 2, 0.01),
  },
} as const;

const Hex = v.pipe(v.string(), v.regex(/^#[0-9a-fA-F]{6}$/));

export const TuningSchema = v.object({
  orb: v.object({
    accent: Hex,
    soft: Hex,
    blush: Hex,
    size: v.number(),
    energyIdle: v.number(),
    energyListen: v.number(),
    energyThink: v.number(),
    energySpeak: v.number(),
    rippleSpeed: v.number(),
    rippleDensity: v.number(),
    swirl: v.number(),
    drift: v.number(),
    breatheHz: v.number(),
    gloss: v.number(),
    matte: v.number(),
    muteDrain: v.number(),
  }),
  glass: v.object({
    tint: Hex,
    tintAlpha: v.number(),
    refThickness: v.number(),
    refFactor: v.number(),
    refDistance: v.number(),
    refDispersion: v.number(),
    refFresnelRange: v.number(),
    refFresnelHardness: v.number(),
    refFresnelFactor: v.number(),
    glareRange: v.number(),
    glareHardness: v.number(),
    glareFactor: v.number(),
    glareConvergence: v.number(),
    glareOppositeFactor: v.number(),
    glareAngle: v.number(),
    blurRadius: v.number(),
    blurEdge: v.boolean(),
    roundness: v.number(),
    mergeRate: v.number(),
    shadowExpand: v.number(),
    shadowFactor: v.number(),
    cover: v.number(),
    domBlur: v.number(),
  }),
  ink: v.object({
    tint: Hex,
    swell: v.number(),
    rippleSpeed: v.number(),
    lifetime: v.number(),
    gloss: v.number(),
  }),
});

export type Tuning = v.InferOutput<typeof TuningSchema>;
export type Section = keyof Tuning;

export function renderTuningModule(t: Tuning): string {
  const body = JSON.stringify(t, null, 2).replace(/"([A-Za-z0-9]+)":/g, "$1:");
  return `// Saved from the studio's "Save as defaults". Hand edits are fine too.
import type { Tuning } from "./tuning-schema.ts";

export const DEFAULT_TUNING = ${body} as const satisfies Tuning;
`;
}

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
    green: color("Pigment · flag green"),
    red: color("Pigment · flag red"),
    indigo: color("Pigment · Bengal indigo"),
    glow: color("Candle light"),
    greenAmount: range("Amount · green", -0.5, 0.5, 0.01),
    redAmount: range("Amount · red", -0.5, 0.5, 0.01),
    indigoAmount: range("Amount · indigo", -0.5, 0.5, 0.01),
    size: range("Size in bar", 28, 64, 1),
    energyIdle: range("Stir · resting", 0, 2, 0.01),
    energyListen: range("Stir · listening", 0, 2, 0.01),
    energyThink: range("Stir · working", 0, 2, 0.01),
    energySpeak: range("Stir · speaking", 0, 2, 0.01),
    rippleSpeed: range("Paint waves · speed (Hz)", 0.02, 2, 0.01),
    swirl: range("Working swirl", 0, 3, 0.01),
    ease: range("State easing", 0.2, 8, 0.1),
    smearScale: range("Smear · blob size", 0.4, 4, 0.01),
    warp: range("Smear · stirring", 0, 2.5, 0.01),
    sharpness: range("Smear · pigment edges", 1, 30, 0.1),
    blur: range("Glass · frost blur", 0, 0.3, 0.001),
    bezel: range("Glass · bezel width", 0, 0.6, 0.01),
    refIndex: range("Glass · refractive index", 1, 2.5, 0.01),
    dispersion: range("Glass · dispersion", 0, 30, 0.1),
    glare: range("Glass · glare", 0, 2, 0.01),
    fresnel: range("Glass · rim", 0, 2, 0.01),
    matte: range("Glass · matte", 0, 1, 0.01),
    glowStrength: range("Candle · strength", 0, 2, 0.01),
    glowRadius: range("Candle · reach", 0.1, 2, 0.01),
    flameWaver: range("Candle · waxing", 0, 1, 0.01),
    flameFlicker: range("Candle · flicker", 0, 1, 0.01),
    flameRate: range("Candle · tempo", 0.1, 3, 0.01),
    flameGust: range("Candle · gusts", 0, 1, 0.01),
    breatheHz: range("Breath at rest (Hz)", 0.05, 1, 0.01),
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
    raise: range("Message field · raised", 0, 2, 0.01),
    orbLight: range("Message field · Rumi's light", 0, 2, 0.01),
    domBlur: range("Page blur under glass", 0, 40, 1),
  },
  ink: {
    tapRipples: toggle("Tap ripples (debug)"),
    tint: color("Slope sheen"),
    swell: range("Swell", 0, 2, 0.01),
    rippleSpeed: range("Ripple speed", 40, 600, 1),
    lifetime: range("Ripple life (s)", 1, 10, 0.1),
    gloss: range("Gloss", 0, 2, 0.01),
    voiceAmp: range("Rumi's waves · height", 0, 4, 0.01),
    voiceWavelength: range("Rumi's waves · length", 40, 600, 1),
    voiceSpeed: range("Rumi's waves · speed", 2, 200, 1),
    voiceReach: range("Rumi's waves · reach", 40, 900, 1),
    voiceEase: range("Rumi's waves · fade rate", 0.1, 4, 0.01),
  },
} as const;

const Hex = v.pipe(v.string(), v.regex(/^#[0-9a-fA-F]{6}$/));

export const TuningSchema = v.object({
  orb: v.object({
    green: Hex,
    red: Hex,
    indigo: Hex,
    glow: Hex,
    greenAmount: v.number(),
    redAmount: v.number(),
    indigoAmount: v.number(),
    size: v.number(),
    energyIdle: v.number(),
    energyListen: v.number(),
    energyThink: v.number(),
    energySpeak: v.number(),
    rippleSpeed: v.number(),
    swirl: v.number(),
    ease: v.number(),
    smearScale: v.number(),
    warp: v.number(),
    sharpness: v.number(),
    blur: v.number(),
    bezel: v.number(),
    refIndex: v.number(),
    dispersion: v.number(),
    glare: v.number(),
    fresnel: v.number(),
    matte: v.number(),
    glowStrength: v.number(),
    glowRadius: v.number(),
    flameWaver: v.number(),
    flameFlicker: v.number(),
    flameRate: v.number(),
    flameGust: v.number(),
    breatheHz: v.number(),
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
    raise: v.number(),
    orbLight: v.number(),
    domBlur: v.number(),
  }),
  ink: v.object({
    tapRipples: v.boolean(),
    tint: Hex,
    swell: v.number(),
    rippleSpeed: v.number(),
    lifetime: v.number(),
    gloss: v.number(),
    voiceAmp: v.number(),
    voiceWavelength: v.number(),
    voiceSpeed: v.number(),
    voiceReach: v.number(),
    voiceEase: v.number(),
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

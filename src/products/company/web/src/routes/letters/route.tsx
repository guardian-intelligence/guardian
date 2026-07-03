import { createFileRoute, Link, Outlet, useParams } from "@tanstack/react-router";
import { type CSSProperties, type ReactNode } from "react";
import { AppChrome } from "@guardian/brand";
import { TopNav } from "~/components/top-nav";
import { criticalTreatmentHead, criticalTreatmentRootStyle } from "~/lib/critical-treatment";
import { lettersBodyFont, lettersTypographyCss } from "~/features/letters/fonts";
import lettersCriticalCss from "~/styles/critical/letters.css?inline";

// Letters layout — /letters and /letters/$slug share this chrome. Paper
// ground, chip Lockup, sepia rule. The layout sets data-treatment so the
// entire subtree (chrome + body + footer) resolves var(--treatment-*) to the
// Letters scope.
//
// The graph paper pattern is checked into app.css as static CSS gradients, so
// first paint does not wait for SVG data-URI decode/filter work. The zones
// below are fixed geometry that remaps the same ruling into a different
// opacity band:
//
//   • reading column (centred max-w-6xl) ........ quiet, readable
//   • page margins (outside that column) ........ gently stronger
//
// The calm masthead region is the area ABOVE a seeded, single-valued,
// circuitous boundary y = f(x), built by function composition:
//
//     boundary = clamp ∘ ( base ⊕ envelope · Σ sineₖ ∘ warp )
//
// base is the straight line between two seeded endpoints (left/right
// Y ∈ [50,300] px, ≥100 px apart — both true by construction); the envelope
// sin(πt) pins those endpoints exactly; the summed sine octaves give the
// wander; the domain warp makes that wander uneven. Every parameter is a
// pure hash read keyed by a label (no mutable RNG), so the edge is
// deterministic per slug. The graph fades in below that curve by stacking
// opacity-weighted clip polygons at increasing y offsets (x in %, y in px so
// the curve never stretches with the document). Above the curve the zone grid
// is clipped away, leaving blank paper, so the masthead band is calm by
// construction.
//
// The layers are position:absolute over the document (NOT viewport fixed)
// and multiply over the ink, so the words read as printed onto this exact
// sheet and it scrolls 1:1 with them. Text is never touched: no blend, no
// clip, no JS — it stays selectable/screen-reader-clean.

export const Route = createFileRoute("/letters")({
  component: LettersLayout,
  // No validateSearch: the developer flags (?developmentMode=1, ?og=1) are set
  // as bare values by raw-history hotkeys (Cmd+D, then "s") and read off the
  // raw URL — validating would re-serialise "1" as quoted and break the check.
  head: () => criticalTreatmentHead("letters", `${lettersCriticalCss}${lettersTypographyCss()}`),
});

// FNV-1a, 32-bit, of an arbitrary string. Math.imul keeps it a true 32-bit
// multiply across engines.
function fnv1a(s: string): number {
  let h = 2166136261 >>> 0;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

// pick: slug → label → Unit ∈ [0,1). A pure hash read, NOT a stateful RNG —
// every rule pulls only the entropy it names, order-independent and
// referentially transparent (identical on every load and SSR/hydration).
const pick = (slug: string, label: string): number => fnv1a(`${slug}:${label}`) / 4294967296;

// --- The seeded circuitous top boundary ---------------------------------
//
// All px. Endpoints live in [Y_MIN, Y_MAX] and are forced ≥ Y_GAP apart;
// the whole curve is clamped to [Y_FLOOR, Y_CEIL] so the wander stays
// bounded.
const Y_MIN = 50;
const Y_MAX = 300;
const Y_GAP = 100;
const Y_FLOOR = 40;
const Y_CEIL = 330;
const SAMPLES = 56;

// endpoints: the ≥100px gap is true by construction. Sample the left Y
// anywhere in [50,300]; sample the right Y across the *feasible set*
// [50,300] \ (lY−100, lY+100) by laying its low band [50, lY−100] and high
// band [lY+100, 300] end to end and mapping one Unit across their combined
// length. No rejection loop; the constraint cannot be violated.
function endpoints(slug: string): { lY: number; rY: number } {
  const lY = Y_MIN + (Y_MAX - Y_MIN) * pick(slug, "edge.l");
  const lowEnd = Math.min(Y_MAX, Math.max(Y_MIN, lY - Y_GAP));
  const highStart = Math.min(Y_MAX, Math.max(Y_MIN, lY + Y_GAP));
  const lowLen = Math.max(0, lowEnd - Y_MIN);
  const highLen = Math.max(0, Y_MAX - highStart);
  const u = pick(slug, "edge.r") * (lowLen + highLen);
  const rY = u < lowLen ? Y_MIN + u : highStart + (u - lowLen);
  return { lY, rY };
}

const TAU = Math.PI * 2;
const clamp = (y: number) => Math.min(Y_CEIL, Math.max(Y_FLOOR, y));

// boundary : t∈[0,1] → y. Pure composition. The envelope is zero at both
// ends so f(0)=lY and f(1)=rY exactly (endpoint rule survives the wander);
// the domain warp reads the octaves at a seeded-skewed parameter so the
// meander is uneven rather than a tidy wave.
function makeBoundary(slug: string): (t: number) => number {
  const { lY, rY } = endpoints(slug);
  const base = (t: number) => lY + (rY - lY) * t;
  const envelope = (t: number) => Math.sin(Math.PI * t);
  const warpAmp = 0.1 + 0.12 * pick(slug, "edge.warpAmp");
  const warpPhase = TAU * pick(slug, "edge.warpPhase");
  const warp = (t: number) => t + warpAmp * Math.sin(TAU * (0.8 * t) + warpPhase);
  const octaveSpec: ReadonlyArray<readonly [baseAmp: number, freq: number]> = [
    [40, 1.3],
    [18, 2.7],
    [9, 5.1],
  ];
  const octaves = octaveSpec.map(([baseAmp, freq], k) => ({
    amp: baseAmp * (0.55 + 0.9 * pick(slug, `edge.amp${k}`)),
    freq,
    phase: TAU * pick(slug, `edge.phase${k}`),
  }));
  const wiggle = (u: number) =>
    octaves.reduce((sum, o) => sum + o.amp * Math.sin(TAU * o.freq * u + o.phase), 0);
  return (t: number) => clamp(base(t) + envelope(t) * wiggle(warp(t)));
}

// The clip region kept on each zone layer: everything BELOW the seeded curve,
// optionally shifted down by a fade stop. x as % (responsive width), y as px
// (no vertical stretch); the 100% rows take the polygon to the true document
// bottom so the grid is solid for the whole scroll once all fade stops apply.
function belowCurveClip(slug: string, offsetY = 0): string {
  const f = makeBoundary(slug);
  const curve = Array.from({ length: SAMPLES }, (_, i) => {
    const t = i / (SAMPLES - 1);
    return `${(t * 100).toFixed(3)}% ${(f(t) + offsetY).toFixed(2)}px`;
  }).join(", ");
  return `polygon(${curve}, 100% 100%, 0% 100%)`;
}

// Cumulative opacity reaches 100% after a six-pitch descent (one ruled line
// of writing per step — the fade unit IS the line pitch, imported from the
// typography it registers to). The first stop is deliberately faint so the
// transition starts as atmosphere, not an outline.
const CURVE_FADE_OPACITIES = [0.05, 0.07, 0.09, 0.12, 0.16, 0.21, 0.3] as const;
const CURVE_FADE_STOPS: ReadonlyArray<Readonly<{ offsetY: number; opacity: number }>> =
  CURVE_FADE_OPACITIES.map((opacity, i) => ({
    offsetY: i * lettersBodyFont.linePitch,
    opacity,
  }));

// Absolute, not fixed: layer is the size of the whole document and scrolls
// with it. No fade-in — the paper paints with the page.
const PAPER_LAYER_CLASS = "pointer-events-none absolute inset-0 z-40";

// Half of max-w-6xl (72rem ≈ 1152px) — the centred reading column's edge —
// and the soft horizontal hand-off across it. Below a ~1152px viewport the
// calc()s cross over and the margin band collapses to nothing → the whole
// sheet is the quiet band (correct on phones).
const PAPER_GEOMETRY_VARS = {
  ["--lp-col" as string]: "576px",
  ["--lp-ramp" as string]: "110px",
} as CSSProperties;

const TEXT_ZONE_MASK =
  "linear-gradient(to right," +
  " transparent 0," +
  " transparent calc(50% - var(--lp-col) - var(--lp-ramp))," +
  " #000 calc(50% - var(--lp-col) + var(--lp-ramp))," +
  " #000 calc(50% + var(--lp-col) - var(--lp-ramp))," +
  " transparent calc(50% + var(--lp-col) + var(--lp-ramp))," +
  " transparent 100%)";

const MARGIN_ZONE_MASK =
  "linear-gradient(to right," +
  " #000 0," +
  " #000 calc(50% - var(--lp-col) - var(--lp-ramp))," +
  " transparent calc(50% - var(--lp-col) + var(--lp-ramp))," +
  " transparent calc(50% + var(--lp-col) - var(--lp-ramp))," +
  " #000 calc(50% + var(--lp-col) + var(--lp-ramp))," +
  " #000 100%)";

function LettersLayout() {
  // strict:false so the layout can read the child route's slug; on the
  // index there is no slug, so the sheet falls back to a stable "letters".
  const params = useParams({ strict: false }) as { slug?: string };
  const slug = params.slug ?? "letters";

  // The seeded boundary, feathered by offset clip-path layers.
  const fadeClips = CURVE_FADE_STOPS.map((stop) => ({
    offsetY: stop.offsetY,
    opacity: stop.opacity,
    clipPath: belowCurveClip(slug, stop.offsetY),
  }));

  return (
    <div
      data-treatment="letters"
      // detail pages get the computed grid phase (fonts.ts registration): the
      // body's first baseline sits ON a rule, statically — no measuring JS.
      data-letters-page={params.slug ? "detail" : "index"}
      className="relative flex min-h-svh flex-col bg-[var(--treatment-ground)] text-[var(--treatment-ink)]"
      style={{
        ...criticalTreatmentRootStyle("letters"),
        isolation: "isolate",
        ...PAPER_GEOMETRY_VARS,
      }}
    >
      <HandFilterDefs slug={slug} />
      <PaperSplotches slug={slug} />
      <PaperWash />
      <PaperGrain />
      <FeatheredGridLayer
        toneClass="letters-paper-grid--text"
        zoneMask={TEXT_ZONE_MASK}
        fadeClips={fadeClips}
      />
      <FeatheredGridLayer
        toneClass="letters-paper-grid--margin"
        zoneMask={MARGIN_ZONE_MASK}
        fadeClips={fadeClips}
      />
      <div className="relative z-10 flex flex-1 flex-col">
        <AppChrome
          treatment="letters"
          LinkComponent={LinkAdapter}
          slotRight={<TopNav />}
          wordmarkHref="/letters"
          sticky={false}
        />
        <main id="main" className="flex-1">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

function PaperGrain() {
  return <div aria-hidden className={`${PAPER_LAYER_CLASS} letters-paper-tooth`} />;
}

// The hand's displacement field, referenced by the letters typography CSS
// (fonts.ts) as filter:url(#letters-hand-filter). Two stages, both Perlin
// noise (feTurbulence is the specced, seeded Perlin function — deterministic
// markup, deterministic render):
//
//   • lean — a long-wavelength (~70px across, ~200px down) field, displacing
//     VERTICALLY ONLY: the feComponentTransfer pins the R channel (the X
//     displacement input) to neutral 0.5. At word scale the ramp across a
//     word reads as the word leaning a degree or so while staying anchored
//     to the ruling — the per-word tilt of the original pages, without
//     per-glyph markup.
//   • fibre — a ~3px-wavelength field at ±0.25px roughening glyph edges,
//     ink wicking into paper fibre instead of a vector-perfect boundary.
//
// Paint-time only: selection, find-in-page, and screen readers see plain
// text. Seeds are pure hash reads per slug, like every other page fixture.
function HandFilterDefs({ slug }: { slug: string }) {
  const leanSeed = 1 + Math.floor(pick(slug, "hand.lean") * 9973);
  const fibreSeed = 1 + Math.floor(pick(slug, "hand.fibre") * 9973);
  return (
    <svg aria-hidden width="0" height="0" style={{ position: "absolute" }}>
      <filter
        id="letters-hand-filter"
        x="-5%"
        y="-5%"
        width="110%"
        height="110%"
        colorInterpolationFilters="sRGB"
      >
        <feTurbulence
          type="fractalNoise"
          baseFrequency="0.014 0.005"
          numOctaves={2}
          seed={leanSeed}
          result="lean"
        />
        <feComponentTransfer in="lean" result="leanY">
          <feFuncR type="linear" slope={0} intercept={0.5} />
        </feComponentTransfer>
        <feDisplacementMap
          in="SourceGraphic"
          in2="leanY"
          scale={1.3}
          xChannelSelector="R"
          yChannelSelector="G"
          result="leaned"
        />
        <feTurbulence
          type="fractalNoise"
          baseFrequency="0.3"
          numOctaves={2}
          seed={fibreSeed}
          result="fibre"
        />
        <feDisplacementMap
          in="leaned"
          in2="fibre"
          scale={0.5}
          xChannelSelector="R"
          yChannelSelector="G"
        />
      </filter>
    </svg>
  );
}

// Sun-fade splotches: the sheet's base is a touch less yellow than it used to
// be, and these pools — drawn in the OLD cream — are where the original tone
// survives. Because the splotch colour IS the previous base, the page can
// only ever sit between the two creams; no blob can go lurid. Deterministic
// per slug via the same pick-by-label reads as the boundary. The layer is
// rendered twice with the complementary column masks: full strength in the
// page margins, dimmed inside the reading column so a pool under the words
// stays atmosphere, never a distraction.
const SPLOTCH_COUNT = 18;
const SPLOTCH_CREAM = "255 244 220"; // the previous base, #fff4dc

function splotchBackground(slug: string): string {
  return Array.from({ length: SPLOTCH_COUNT }, (_, i) => {
    const x = 100 * pick(slug, `splotch.x${i}`);
    const y = 100 * pick(slug, `splotch.y${i}`);
    const w = 200 + 340 * pick(slug, `splotch.w${i}`);
    const h = 160 + 280 * pick(slug, `splotch.h${i}`);
    const a = 0.3 + 0.5 * pick(slug, `splotch.a${i}`);
    return (
      `radial-gradient(${w.toFixed(0)}px ${h.toFixed(0)}px at ${x.toFixed(2)}% ${y.toFixed(2)}%,` +
      `rgb(${SPLOTCH_CREAM} / ${a.toFixed(3)}) 0%,` +
      `rgb(${SPLOTCH_CREAM} / ${(a * 0.45).toFixed(3)}) 48%,transparent 72%)`
    );
  }).join(",");
}

function PaperSplotches({ slug }: { slug: string }) {
  const backgroundImage = splotchBackground(slug);
  const layer = (mask: string, opacity?: number) => (
    <div
      aria-hidden
      className="pointer-events-none absolute inset-0 z-0"
      style={{
        backgroundImage,
        backgroundRepeat: "no-repeat",
        opacity,
        WebkitMaskImage: mask,
        maskImage: mask,
      }}
    />
  );
  return (
    <>
      {layer(MARGIN_ZONE_MASK)}
      {layer(TEXT_ZONE_MASK, 0.35)}
    </>
  );
}

// Watercolor wash: soft splotches where the cream sheet dried lighter, toward
// white. Pure CSS radial gradients (see .letters-paper-wash) — no SVG filter,
// no blend mode, no animation — so it paints once and costs nothing at runtime.
// Sits at z-0, BELOW the text, so white alpha only lightens paper and never
// washes out ink; the graph ruling (z-40, multiply) still prints over the
// splotches.
function PaperWash() {
  return (
    <div aria-hidden className="pointer-events-none absolute inset-0 z-0 letters-paper-wash" />
  );
}

// One band of the sheet: the checked-in CSS grid, shown only inside its
// horizontal zone and feathered below the seeded boundary.
function FeatheredGridLayer({
  toneClass,
  zoneMask,
  fadeClips,
}: {
  toneClass: string;
  zoneMask: string;
  fadeClips: ReadonlyArray<Readonly<{ offsetY: number; opacity: number; clipPath: string }>>;
}) {
  return (
    <>
      {fadeClips.map((stop) => (
        <GridLayer
          key={stop.offsetY}
          toneClass={toneClass}
          zoneMask={zoneMask}
          clipPath={stop.clipPath}
          opacity={stop.opacity}
        />
      ))}
    </>
  );
}

// aria-hidden decoration; the text DOM is not clipped or blended.
function GridLayer({
  toneClass,
  zoneMask,
  clipPath,
  opacity,
}: {
  toneClass: string;
  zoneMask: string;
  clipPath: string;
  opacity: number;
}) {
  return (
    <div
      aria-hidden
      className={`${PAPER_LAYER_CLASS} letters-paper-grid ${toneClass}`}
      style={{
        opacity,
        WebkitMaskImage: zoneMask,
        maskImage: zoneMask,
        clipPath,
        WebkitClipPath: clipPath,
      }}
    />
  );
}

function LinkAdapter(props: {
  to: string;
  className?: string;
  style?: React.CSSProperties;
  "aria-label"?: string;
  onClick?: React.MouseEventHandler;
  children?: ReactNode;
}) {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return <Link {...(props as any)} />;
}

import { Lockup as HouseLockup, type LockupSize } from "@guardian/brand/components/lockup";
import { WINGS_PATH_D, WINGS_TIGHT_VIEWBOX } from "@guardian/brand/components/wings";
import { accents } from "@guardian/brand/tokens";

// Guardian wings, argent; positioned inside a parent SVG.
export function Wings({ x, y, size }: { x: number; y: number; size: number }) {
  return (
    <svg x={x - size / 2} y={y - size / 2} width={size} height={size} viewBox={WINGS_TIGHT_VIEWBOX}>
      <path d={WINGS_PATH_D} fill={accents.argent} />
    </svg>
  );
}

// The Guardian lockup with the Anveio wordmark: chip on paper, bare argent
// wings on ink. The wordmark is set at 800 so its 2px stems match the
// masthead rule; the mark-to-word gap is the slide head's clearspace.
export function Lockup({ size = "sm", onInk = false }: { size?: LockupSize; onInk?: boolean }) {
  return (
    <HouseLockup
      size={size}
      variant={onInk ? "argent" : "chip"}
      wordmark={<span style={{ fontWeight: 800 }}>Anveio</span>}
      wordmarkColor={onInk ? accents.argent : accents.ink}
      title="Anveio"
      className="deck-lockup"
      style={{ padding: 0, gap: "var(--head-gap)" }}
    />
  );
}

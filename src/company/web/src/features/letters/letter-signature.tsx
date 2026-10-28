import type { Letter } from "~/content/letters";
import { HandwrittenSignature } from "~/features/letters/handwritten-signature";

// The sign-off, by letter kind. A dispatch — a letter from the author to a
// younger self — closes with his hand:
// The dispatch sign-off is a rectified trace of the author's ink signature.
// The SVG carries the accessible name so the close is readable and indexable.
//
// Correspondence (dear-shovon, written by someone else) is signed by a hand we
// don't reveal: a redaction bar struck in the letter's ink where the name
// would be — twice the footprint of the name it covers, a deliberate strike.
// The body already carries the sender's own 💙 close just above it. The faint
// paper grid multiplies over the bar so it reads as a marker stroke on the
// sheet; the slight rotation keeps it a drawn gesture, not a sterile box.
export function LetterSignature({ letter }: { readonly letter: Letter }) {
  if (letter.kind === "dispatch") {
    return (
      <div className="mt-16" style={{ marginLeft: "72px" }}>
        <HandwrittenSignature className="block h-auto w-[min(100%,clamp(180px,25vw,252px))] text-[var(--treatment-ink)]" />
        <p
          aria-label="By Shovon Hasan - Founder, Guardian. Est. 2026 Seattle, WA"
          className="mt-3 max-w-[min(100%,clamp(180px,25vw,252px))] text-right font-display text-[clamp(10.5px,0.8vw,12px)] leading-[1.35] tracking-[0.02em] text-[var(--treatment-muted-strong)]"
        >
          <span className="block text-[var(--treatment-ink)]">By Shovon Hasan</span>{" "}
          <span className="block">Founder, Guardian</span>{" "}
          <span className="block">Est. 2026 · Seattle, WA</span>
        </p>
      </div>
    );
  }
  return (
    <div
      aria-hidden
      className="font-display text-[clamp(20px,1.6vw,22px)]"
      style={{ marginTop: "8px" }}
    >
      <span
        style={{
          display: "inline-block",
          width: "8ch",
          height: "1.64em",
          background: "var(--treatment-ink)",
          borderRadius: "2px",
          transform: "rotate(-0.5deg)",
        }}
      />
    </div>
  );
}

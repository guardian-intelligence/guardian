import { WingsArgent } from "@guardian/brand";

// Guardian mark centered — the one fixed brand element on the page. Shortty
// is the product; Guardian is the maker's mark above the stage.
export function Header() {
  return (
    <header className="relative flex items-center justify-center pt-8 pb-2">
      <a
        href="https://guardianintelligence.org"
        aria-label="Guardian Intelligence"
        className="opacity-70 transition-opacity hover:opacity-100"
      >
        <WingsArgent className="h-9 w-9" />
      </a>
    </header>
  );
}

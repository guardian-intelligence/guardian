import { WingsArgent } from "@guardian/brand";

export function Header() {
  return (
    <header className="shortty-header">
      <a
        href="https://guardianintelligence.org"
        aria-label="Guardian Intelligence"
        className="shortty-header__maker"
      >
        Guardian
      </a>
      <WingsArgent className="shortty-header__mark" aria-hidden="true" />
      <span className="shortty-header__status">
        <span className="shortty-header__status-dot" aria-hidden="true" />
        Local only
      </span>
    </header>
  );
}

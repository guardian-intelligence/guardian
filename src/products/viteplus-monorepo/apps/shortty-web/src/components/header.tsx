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
      <span className="shortty-header__orb" aria-hidden="true">
        <WingsArgent className="shortty-header__mark shortty-header__mark--base" />
        <WingsArgent className="shortty-header__mark shortty-header__mark--reflection" />
      </span>
      <span className="shortty-header__right">
        <a
          href="https://github.com/guardian-intelligence/guardian"
          target="_blank"
          rel="noreferrer"
          aria-label="View Guardian on GitHub"
          className="shortty-header__glow-control shortty-header__github"
        >
          <span className="shortty-header__glow-effect" aria-hidden="true" />
          <span className="shortty-header__glow-text">
            <svg viewBox="0 0 24 24" aria-hidden="true">
              <path
                fill="currentColor"
                d="M12 .7A11.5 11.5 0 0 0 8.36 23.1c.58.1.79-.25.79-.56v-2.2c-3.22.7-3.9-1.37-3.9-1.37-.52-1.34-1.28-1.69-1.28-1.69-1.05-.72.08-.7.08-.7 1.16.08 1.77 1.19 1.77 1.19 1.03 1.77 2.7 1.26 3.36.96.1-.75.4-1.26.73-1.55-2.57-.29-5.27-1.28-5.27-5.69 0-1.26.45-2.29 1.19-3.1-.12-.29-.52-1.47.11-3.06 0 0 .97-.31 3.16 1.18A10.9 10.9 0 0 1 12 6.12c.98 0 1.95.13 2.86.39 2.2-1.49 3.16-1.18 3.16-1.18.63 1.59.23 2.77.11 3.06.74.81 1.19 1.84 1.19 3.1 0 4.42-2.71 5.39-5.29 5.68.42.36.79 1.07.79 2.16v3.21c0 .31.21.67.8.56A11.5 11.5 0 0 0 12 .7Z"
              />
            </svg>
          </span>
        </a>
        <span className="shortty-header__glow-control shortty-header__status">
          <span className="shortty-header__glow-effect" aria-hidden="true" />
          <span className="shortty-header__glow-text">
            <span className="shortty-header__status-dot" aria-hidden="true" />
            Local only
          </span>
        </span>
      </span>
    </header>
  );
}

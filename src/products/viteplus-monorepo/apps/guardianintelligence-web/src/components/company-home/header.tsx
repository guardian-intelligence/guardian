import { Link } from "@tanstack/react-router";
import { Lockup, WingsArgent } from "@guardian/brand";
import { TopNav } from "~/components/top-nav";

const REPOSITORY_URL = "https://github.com/guardian-intelligence/guardian";

export function CompanyHomeHeader() {
  return (
    <header className="company-home-header">
      <Link to="/" aria-label="Guardian — home" className="company-home-header__lockup">
        <Lockup size="sm" variant="argent" title="Guardian" style={{ padding: 0 }} />
      </Link>
      <a
        href={REPOSITORY_URL}
        target="_blank"
        rel="noreferrer"
        aria-label="Guardian Intelligence on GitHub"
        className="company-home-header__orb"
        data-illumination-source="logo"
      >
        <WingsArgent className="company-home-header__mark company-home-header__mark--base" />
        <WingsArgent className="company-home-header__mark company-home-header__mark--reflection" />
      </a>
      <div className="company-home-header__right">
        <TopNav />
      </div>
    </header>
  );
}

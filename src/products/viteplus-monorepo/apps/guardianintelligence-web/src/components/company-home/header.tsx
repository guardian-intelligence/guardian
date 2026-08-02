import { Link } from "@tanstack/react-router";
import { Lockup } from "@guardian/brand";
import { TopNav } from "~/components/top-nav";

export function CompanyHomeHeader() {
  return (
    <header className="company-home-header">
      <Link to="/" aria-label="Guardian — home" className="company-home-header__lockup">
        <Lockup size="sm" variant="argent" title="Guardian" style={{ padding: 0 }} />
      </Link>
      <div className="company-home-header__right">
        <TopNav />
      </div>
    </header>
  );
}

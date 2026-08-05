import { Link } from "@tanstack/react-router";
import { WingsArgent } from "@guardian/brand";
import { TopNav } from "~/components/top-nav";
import { OutlinedHeaderWordmark } from "./outlined-wordmarks";

export function CompanyHomeHeader() {
  return (
    <header className="company-home-header">
      <Link to="/" aria-label="Guardian — home" className="company-home-header__lockup">
        <WingsArgent
          className="company-home-header__mark"
          viewBoxMode="padded"
          wingsScale={1.21}
          aria-hidden="true"
        />
        <OutlinedHeaderWordmark className="company-home-header__wordmark" />
      </Link>
      <div className="company-home-header__right">
        <TopNav />
      </div>
    </header>
  );
}

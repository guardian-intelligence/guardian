import { Link } from "@tanstack/react-router";
import { WingsArgent } from "@guardian/brand";
import { useEffect, useState } from "react";
import { landing } from "~/content/landing";
import "~/styles/company-home.css";
import { CompanyHomeHeader } from "./header";
import { HeroMaterialization } from "./hero-materialization";
import { IlluminationDocument } from "./illumination-document";

const REPOSITORY_URL = "https://github.com/guardian-intelligence/guardian";

export function PersistentCompanyHome({ active }: { readonly active: boolean }) {
  const [hasMounted, setHasMounted] = useState(active);

  useEffect(() => {
    if (active) setHasMounted(true);
  }, [active]);

  if (!active && !hasMounted) return null;

  return (
    <div
      className="persistent-company-home"
      data-company-home-active={active ? "true" : "false"}
      aria-hidden={active ? undefined : true}
      inert={!active}
      role={active ? "main" : undefined}
      id={active ? "main" : undefined}
    >
      <IlluminationDocument active={active}>
        <div className="company-home-shell">
          <CompanyHomeHeader />
          <a
            href={REPOSITORY_URL}
            target="_blank"
            rel="noreferrer"
            aria-label="Guardian Intelligence on GitHub"
            className="company-home-beacon"
            data-illumination-source="logo"
          >
            <WingsArgent className="company-home-beacon__mark company-home-beacon__mark--base" />
            <WingsArgent className="company-home-beacon__mark company-home-beacon__mark--reflection" />
          </a>
          <div className="company-home-main">
            <section className="company-home-hero" aria-labelledby="company-home-title">
              <div className="company-home-blueprint" aria-hidden="true">
                <span
                  className="company-home-blueprint__rail company-home-blueprint__rail--top"
                  data-blueprint-rail="top"
                />
                <span
                  className="company-home-blueprint__rail company-home-blueprint__rail--right"
                  data-blueprint-rail="right"
                />
                <span
                  className="company-home-blueprint__rail company-home-blueprint__rail--bottom"
                  data-blueprint-rail="bottom"
                />
                <span
                  className="company-home-blueprint__rail company-home-blueprint__rail--left"
                  data-blueprint-rail="left"
                />
                <span
                  className="company-home-blueprint__rail company-home-blueprint__rail--divider"
                  data-blueprint-rail="divider"
                />
              </div>
              <div className="company-home-hero__eyebrow">
                <span>{landing.kicker}</span>
              </div>
              <div className="company-home-hero__copy-frame">
                <span
                  className="company-home-hero__cross company-home-hero__cross--left"
                  aria-hidden="true"
                />
                <span
                  className="company-home-hero__cross company-home-hero__cross--right"
                  aria-hidden="true"
                />
                <HeroMaterialization active={active} label={landing.hero} />
                <p className="company-home-hero__lede">{landing.lede}</p>
                <Link to="/contact" className="company-home-contact-link">
                  <span>Get in touch</span>
                  <span aria-hidden="true">↗</span>
                </Link>
              </div>
            </section>
          </div>
        </div>
      </IlluminationDocument>
    </div>
  );
}

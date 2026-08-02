import { createFileRoute, Link } from "@tanstack/react-router";
import { WingsArgent } from "@guardian/brand";
import {
  COMPANY_EXPERIENCE_BOOTSTRAP,
  prepareCompanyExperience,
} from "~/components/company-home/company-experience";
import { HeroMaterialization } from "~/components/company-home/hero-materialization";
import { IlluminationDocument } from "~/components/company-home/illumination-document";
import { CompanyHomeHeader } from "~/components/company-home/header";
import { landing } from "~/content/landing";
import { canonicalLink, ogMeta } from "~/lib/head";
import "~/styles/company-home.css";

const REPOSITORY_URL = "https://github.com/guardian-intelligence/guardian";

export const Route = createFileRoute("/_workshop/")({
  beforeLoad: () => prepareCompanyExperience(),
  component: LandingPage,
  head: () => ({
    meta: ogMeta({
      slug: "home",
      title: "Guardian Intelligence",
      description:
        "Guardian Intelligence builds reusable software in the open and private systems under enterprise contract.",
      path: "/",
      imageFormat: "png",
    }),
    links: [canonicalLink("/")],
    scripts: [{ children: COMPANY_EXPERIENCE_BOOTSTRAP }],
  }),
});

function LandingPage() {
  return (
    <IlluminationDocument>
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
              <HeroMaterialization label={landing.hero} />
              <p className="company-home-hero__lede">{landing.lede}</p>
              <Link to="/contact" className="company-home-cta" data-illumination-glass="control">
                <span className="company-home-cta__text">Request Software</span>
                <span aria-hidden="true">↗</span>
              </Link>
            </div>
          </section>
        </div>
      </div>
    </IlluminationDocument>
  );
}

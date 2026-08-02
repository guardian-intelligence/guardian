import { createFileRoute, Link } from "@tanstack/react-router";
import { IlluminationDocument } from "~/components/company-home/illumination-document";
import { CompanyHomeHeader } from "~/components/company-home/header";
import { landing } from "~/content/landing";
import { canonicalLink, ogMeta } from "~/lib/head";
import "~/styles/company-home.css";

export const Route = createFileRoute("/_workshop/")({
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
  }),
});

function LandingPage() {
  return (
    <IlluminationDocument>
      <div className="company-home-shell">
        <CompanyHomeHeader />
        <div className="company-home-main">
          <section className="company-home-hero" aria-labelledby="company-home-title">
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
              <h1
                id="company-home-title"
                className="company-home-title"
                data-copy={landing.hero}
                aria-label={landing.hero}
              >
                <span className="company-home-title__base">{landing.hero}</span>
              </h1>
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

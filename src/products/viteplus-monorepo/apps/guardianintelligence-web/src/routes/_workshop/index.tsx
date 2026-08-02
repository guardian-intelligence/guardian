import { createFileRoute } from "@tanstack/react-router";
import { IlluminationDocument } from "~/components/company-home/illumination-document";
import { CompanyHomeHeader } from "~/components/company-home/header";
import { SoftwareRequestForm } from "~/components/company-home/software-request-form";
import { landing } from "~/content/landing";
import { canonicalLink, ogMeta } from "~/lib/head";
import "~/styles/company-home.css";

export const Route = createFileRoute("/_workshop/")({
  component: LandingPage,
  head: () => ({
    meta: ogMeta({
      slug: "home",
      title: "Guardian Intelligence — Request any software",
      description:
        "Guardian Intelligence is a forward-deployed intelligence company. Request open-source software for your company or engage us for a private enterprise build.",
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
            </div>
            <div className="company-home-hero__form-frame">
              <SoftwareRequestForm />
              <details className="company-home-hero__more">
                <summary>Explore what we already build</summary>
                <div>
                  <a href="/postflight">
                    <strong>Postflight</strong>
                    <span>CI with a speed commitment.</span>
                  </a>
                  <a href="mailto:sales@guardianintelligence.org?subject=Enterprise%20software%20request">
                    <strong>Enterprise</strong>
                    <span>Private software, built for your company.</span>
                  </a>
                </div>
              </details>
            </div>
          </section>
        </div>
      </div>
    </IlluminationDocument>
  );
}

import { Link } from "@tanstack/react-router";
import { WingsArgent } from "@guardian/brand";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { landing } from "~/content/landing";
import "~/styles/company-home.css";
import { CompanyHomeHeader } from "./header";
import { HeroMaterialization } from "./hero-materialization";
import { IlluminationDocument } from "./illumination-document";

const REPOSITORY_URL = "https://github.com/guardian-intelligence/guardian";

function useBlueprintGeometry(enabled: boolean) {
  const blueprintRef = useRef<HTMLDivElement>(null);
  const eyebrowRef = useRef<HTMLDivElement>(null);
  const frameRef = useRef<HTMLDivElement>(null);
  const ledeRef = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    if (!enabled) return;
    const blueprint = blueprintRef.current;
    const eyebrow = eyebrowRef.current;
    const frame = frameRef.current;
    const lede = ledeRef.current;
    if (!blueprint || !eyebrow || !frame || !lede) return;

    let animationFrame = 0;
    const measure = () => {
      animationFrame = 0;
      const frameRect = frame.getBoundingClientRect();
      const eyebrowRect = eyebrow.getBoundingClientRect();
      const ledeRect = lede.getBoundingClientRect();
      blueprint.style.setProperty("--company-blueprint-left", `${frameRect.left}px`);
      blueprint.style.setProperty("--company-blueprint-right", `${frameRect.right - 1}px`);
      blueprint.style.setProperty("--company-blueprint-top", `${frameRect.top}px`);
      blueprint.style.setProperty("--company-blueprint-divider", `${eyebrowRect.bottom}px`);
      blueprint.style.setProperty("--company-blueprint-lede-top", `${ledeRect.top}px`);
      blueprint.style.setProperty("--company-blueprint-bottom", `${ledeRect.bottom - 1}px`);
      blueprint.dataset.blueprintGeometry = "ready";
    };
    const scheduleMeasure = () => {
      if (animationFrame) window.cancelAnimationFrame(animationFrame);
      animationFrame = window.requestAnimationFrame(measure);
    };
    const resizeObserver = new ResizeObserver(scheduleMeasure);
    const scroller = blueprint
      .closest(".illumination-document")
      ?.querySelector(".illumination-document__content");
    resizeObserver.observe(frame);
    resizeObserver.observe(eyebrow);
    resizeObserver.observe(lede);
    scroller?.addEventListener("scroll", scheduleMeasure, { passive: true });
    window.addEventListener("resize", scheduleMeasure, { passive: true });
    measure();

    return () => {
      if (animationFrame) window.cancelAnimationFrame(animationFrame);
      resizeObserver.disconnect();
      scroller?.removeEventListener("scroll", scheduleMeasure);
      window.removeEventListener("resize", scheduleMeasure);
    };
  }, [enabled]);

  return { blueprintRef, eyebrowRef, frameRef, ledeRef };
}

export function PersistentCompanyHome({ active }: { readonly active: boolean }) {
  const [hasMounted, setHasMounted] = useState(active);
  const { blueprintRef, eyebrowRef, frameRef, ledeRef } = useBlueprintGeometry(hasMounted);

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
              <div ref={frameRef} className="company-home-hero__blueprint-frame">
                <div ref={blueprintRef} className="company-home-blueprint" aria-hidden="true">
                  <span
                    className="company-home-blueprint__rail company-home-blueprint__rail--top"
                    data-blueprint-rail="top"
                  />
                  <span
                    className="company-home-blueprint__rail company-home-blueprint__rail--right"
                    data-blueprint-rail="right"
                  />
                  <span
                    className="company-home-blueprint__rail company-home-blueprint__rail--left"
                    data-blueprint-rail="left"
                  />
                  <span
                    className="company-home-blueprint__rail company-home-blueprint__rail--divider"
                    data-blueprint-rail="divider"
                  />
                  <span
                    className="company-home-blueprint__rail company-home-blueprint__rail--lede-top"
                    data-blueprint-rail="lede-top"
                  />
                  <span
                    className="company-home-blueprint__rail company-home-blueprint__rail--lede-bottom"
                    data-blueprint-rail="lede-bottom"
                  />
                  <span className="company-home-blueprint__guide company-home-blueprint__guide--left" />
                  <span className="company-home-blueprint__guide company-home-blueprint__guide--right" />
                </div>
                <span
                  className="company-home-hero__cross company-home-hero__cross--left"
                  aria-hidden="true"
                />
                <span
                  className="company-home-hero__cross company-home-hero__cross--right"
                  aria-hidden="true"
                />
                <div ref={eyebrowRef} className="company-home-hero__eyebrow">
                  <span>{landing.kicker}</span>
                </div>
                <div className="company-home-hero__copy-frame">
                  <div className="company-home-hero__title-frame">
                    <HeroMaterialization active={active} label={landing.hero} />
                  </div>
                  <div ref={ledeRef} className="company-home-hero__lede-frame">
                    <p className="company-home-hero__lede">{landing.lede}</p>
                  </div>
                </div>
              </div>
              <Link to="/contact" className="company-home-contact-link">
                <span>Get in touch</span>
                <span aria-hidden="true">↗</span>
              </Link>
            </section>
          </div>
        </div>
      </IlluminationDocument>
    </div>
  );
}

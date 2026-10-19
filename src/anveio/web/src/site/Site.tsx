import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { Lockup } from "~/deck/Brand";

// The company pages' frame: the lockup and the deck link along the top, the
// legal line and the company pages along the bottom. Laid out by CSS alone
// (styles/site.css) so the server-rendered page is already final.
export function Site({ children }: { children: ReactNode }) {
  return (
    <div className="site">
      <header className="site-bar">
        <Link to="/" className="site-home" aria-label="Anveio home">
          <Lockup />
        </Link>
        <nav aria-label="Primary">
          <Link to="/deck" className="site-nav-link">
            Deck
          </Link>
        </nav>
      </header>
      <main className="site-main">{children}</main>
      <footer className="site-foot">
        <p>© Anveio Foundation</p>
        <nav aria-label="Company">
          <Link to="/contact">Contact</Link>
          <Link to="/privacy">Privacy</Link>
        </nav>
      </footer>
    </div>
  );
}

// Per-page head: title, description and canonical URL, indexable.
export function pageHead({ path, title, description }: PageMeta) {
  const url = `https://anveio.com${path}`;
  return {
    meta: [
      { title },
      { name: "description", content: description },
      { property: "og:type", content: "website" },
      { property: "og:site_name", content: "Anveio" },
      { property: "og:url", content: url },
      { property: "og:title", content: title },
      { property: "og:description", content: description },
      { name: "twitter:card", content: "summary" },
      { name: "twitter:title", content: title },
      { name: "twitter:description", content: description },
    ],
    links: [{ rel: "canonical", href: url }],
  };
}

type PageMeta = { readonly path: string; readonly title: string; readonly description: string };

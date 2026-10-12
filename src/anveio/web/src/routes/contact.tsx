import { createFileRoute } from "@tanstack/react-router";
import { pageHead, Site } from "~/site/Site";

export const Route = createFileRoute("/contact")({
  component: Contact,
  head: () =>
    pageHead({
      path: "/contact",
      title: "Contact · Anveio",
      description: "How to reach Anveio Foundation.",
    }),
});

// Only addresses with a forward rule in Cloudflare Email Routing
// (src/infrastructure/bootstrap/guardian-mgmt-dns/main.tf); the anveio.com
// catch-all drops everything else.
export function Contact() {
  return (
    <Site>
      <article className="site-page">
        <h1 className="site-page-title">Contact</h1>
        <dl className="site-contact">
          <div>
            <dt>Shovon Hasan, founder</dt>
            <dd>
              <a href="mailto:shovon@anveio.com">shovon@anveio.com</a>
            </dd>
          </div>
          <div>
            <dt>Careers</dt>
            <dd>
              <a href="mailto:careers@anveio.com">careers@anveio.com</a>
            </dd>
          </div>
          <div>
            <dt>Book time with the founder</dt>
            <dd>
              <a href="/shovon">anveio.com/shovon</a>
            </dd>
          </div>
        </dl>
        <p className="site-note">
          Anveio Foundation is a Delaware corporation. For privacy requests, write to{" "}
          <a href="mailto:shovon@anveio.com">shovon@anveio.com</a>.
        </p>
      </article>
    </Site>
  );
}

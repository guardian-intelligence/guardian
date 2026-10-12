import { createFileRoute } from "@tanstack/react-router";
import { pageHead, Site } from "~/site/Site";

// The policy for all of anveio.com. Google's OAuth consent screen for the
// anveio-cal client links this URL, so it must stay at /privacy and keep
// covering the sign-in on /shovon. Every line describes what the code does
// today: src/anveio/cal/src/auth/google.ts for the sign-in, the ingress logs
// (src/infrastructure/base/apps/observability.yaml keeps logs three months)
// and the anveio.com Email Routing rules in guardian-mgmt-dns.
export const Route = createFileRoute("/privacy")({
  component: Privacy,
  head: () =>
    pageHead({
      path: "/privacy",
      title: "Privacy Policy · Anveio",
      description: "What anveio.com collects, where it goes and how long it is kept.",
    }),
});

const INVENTORY = [
  {
    what: "Request details: IP address, browser user agent, the page asked for, the referring page and the time",
    when: "Every visit to any page",
    where:
      "Cloudflare, which carries all traffic to the site, and the access logs of our own servers",
    kept: "Our logs: up to three months, then deleted automatically. Cloudflare: under its own policy",
  },
  {
    what: "Your Google account ID, name, email address and profile picture link",
    when: "Only if you choose Sign in with Google on anveio.com/shovon",
    where:
      "Nowhere: Google hands them to your browser, which checks them and keeps them in the open page",
    kept: "Until you close or reload the page",
  },
  {
    what: "Emails you send us, with your address and anything you include",
    when: "Only if you write to an anveio.com address",
    where: "Forwarded by Cloudflare Email Routing to the founder’s Gmail inbox",
    kept: "As long as needed to answer you, or until you ask us to delete them",
  },
] as const;

export function Privacy() {
  return (
    <Site>
      <article className="site-page site-prose">
        <h1 className="site-page-title">Privacy Policy</h1>
        <p className="site-note">Effective October 12, 2026</p>

        <p>
          This policy covers anveio.com, run by Anveio Foundation (“Anveio”, “we”), a Delaware
          corporation. That includes this website, the Anveio deck at anveio.com/deck, and
          anveio.com/shovon, our founder Shovon Hasan’s contact card, where Rumi, an assistant,
          takes messages and schedules time with him.
        </p>

        <h2>What we collect</h2>
        <div className="site-table-wrap">
          <table className="site-table">
            <thead>
              <tr>
                <th scope="col">What</th>
                <th scope="col">When</th>
                <th scope="col">Where it goes</th>
                <th scope="col">How long</th>
              </tr>
            </thead>
            <tbody>
              {INVENTORY.map((row) => (
                <tr key={row.what}>
                  <td>{row.what}</td>
                  <td>{row.when}</td>
                  <td>{row.where}</td>
                  <td>{row.kept}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p>
          That is everything. The site sets no cookies, stores nothing in your browser and runs no
          analytics or advertising. The only outside service a page talks to is Google, and only
          when you sign in with Google.
        </p>

        <h2>Sign in with Google</h2>
        <p>
          On anveio.com/shovon you can sign in with Google so Rumi knows who is leaving a message.
          We ask Google only for your basic profile: your Google account ID, name, email address and
          profile picture. Google sends these to your browser, never to our servers. The page checks
          them against Google’s published keys, holds them while it is open and forgets them when
          you close or reload it. We don’t receive your password or access to anything else in your
          Google account.
        </p>
        <p>
          We use information received from Google APIs only to sign you in. Our use adheres to the{" "}
          <a href="https://developers.google.com/terms/api-services-user-data-policy">
            Google API Services User Data Policy
          </a>
          , including the Limited Use requirements. You can remove anveio.com from your Google
          account at any time in your{" "}
          <a href="https://myaccount.google.com/connections">Google Account settings</a>.
        </p>

        <h2>Messages to Rumi</h2>
        <p>
          Rumi is a prototype. Today she doesn’t use your microphone, and nothing you say or type on
          anveio.com/shovon is recorded, sent or stored. Before she starts taking real voice or text
          messages, we will update this policy to say what is kept, where and for how long.
        </p>

        <h2>What we don’t do</h2>
        <p>
          We don’t sell your information or use it for advertising. We may disclose information if
          the law requires it.
        </p>

        <h2>Your choices</h2>
        <p>
          You can ask to see, correct or delete anything we hold about you by writing to{" "}
          <a href="mailto:shovon@anveio.com">shovon@anveio.com</a>.
        </p>

        <h2>Children</h2>
        <p>
          This site isn’t meant for children under 13, and we don’t knowingly collect their
          information.
        </p>

        <h2>Changes</h2>
        <p>If this policy changes, we’ll update it here and change the date at the top.</p>

        <h2>Contact</h2>
        <p>
          Anveio Foundation, <a href="mailto:shovon@anveio.com">shovon@anveio.com</a>.
        </p>
      </article>
    </Site>
  );
}

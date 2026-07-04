const STAGES = ["beta", "gamma", "prod"] as const;

type Stage = (typeof STAGES)[number];

interface FaviconLink {
  readonly rel: string;
  readonly type?: string;
  readonly sizes?: string;
  readonly href: string;
}

// One immutable image serves every stage (beta.gi.org, gamma.gi.org, the
// apex), so the favicon cannot be chosen at build time. During SSR the
// deployment's declared identity (GUARDIAN_SITE, same contract as
// server-deploy-meta.ts) picks the set; after hydration the client re-derives
// the same answer from the hostname it is actually served on, so head()
// stays consistent across SSR and client navigation.
function resolveStage(): Stage | null {
  if (import.meta.env.SSR) {
    const site = process.env.GUARDIAN_SITE ?? "";
    return (STAGES as readonly string[]).includes(site) ? (site as Stage) : null;
  }
  const host = window.location.hostname;
  if (host === "guardianintelligence.org" || host === "www.guardianintelligence.org") {
    return "prod";
  }
  if (host.startsWith("beta.")) return "beta";
  if (host.startsWith("gamma.")) return "gamma";
  return null;
}

// Unknown surfaces (pr-<N> previews, localhost dev) fall back to the unbadged
// set at the public root; each stage manifest self-references its own icons.
export function faviconLinks(): FaviconLink[] {
  const stage = resolveStage();
  const base = stage ? `/stage-favicons/${stage}` : "";
  return [
    { rel: "icon", type: "image/svg+xml", href: `${base}/favicon.svg` },
    { rel: "alternate icon", type: "image/x-icon", href: `${base}/favicon.ico` },
    { rel: "apple-touch-icon", sizes: "180x180", href: `${base}/apple-touch-icon.png` },
    { rel: "manifest", href: `${base}/site.webmanifest` },
  ];
}

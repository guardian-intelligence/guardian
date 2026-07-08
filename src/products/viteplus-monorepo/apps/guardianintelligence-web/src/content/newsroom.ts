// Newsroom items — content source for the /news index, the
// /news/$slug article route, and the homepage broadcast strip.
//
// Doctrine (per /design/newsroom): Flare on a homepage or cross-treatment
// surface always means "Guardian is speaking in the public register." If it
// is on the page, something newsworthy sits inside it. No decorative Flare.
// When there is no current item the strip and the index hero both render
// empty states.
//
// Shape: every entry is a full bulletin with a body. Add a new bulletin by
// prepending to ITEMS (the array is reverse-chronological by convention).

import type { TamProjectionDefaults } from "~/features/tam-playground/model";
import {
  DEFAULT_CURRENT_ENTHUSIAST_DEMAND_BILLION,
  DEFAULT_SEGMENT_CAGR_PCT,
} from "~/features/tam-playground/model";

export type NewsroomCategory = "announcement" | "milestone" | "note";

export interface NewsroomAuthor {
  readonly name: string;
  readonly role: string;
  readonly avatar?: string;
}

// An optional interactive module embedded in an article body. Discriminated by
// `kind` so the article route can switch on it; the only kind today is the
// bare-metal TAM scenario playground.
export type NewsroomInteractive = {
  readonly kind: "bare-metal-tam-playground";
  readonly defaults: TamProjectionDefaults;
};

export interface NewsroomItem {
  readonly slug: string;
  readonly kicker: string;
  readonly category: NewsroomCategory;
  readonly title: string;
  readonly deck: string;
  readonly date: string;
  readonly publishedAt: string;
  readonly author: NewsroomAuthor;
  // Optional lead bullets rendered above the body — a scannable summary before
  // the press-release prose.
  readonly lead?: readonly string[];
  readonly body: readonly string[];
  // Optional illustrative infographic rendered below the interactive module.
  readonly infographic?: "guardian-up";
  readonly interactive?: NewsroomInteractive;
  readonly ctaLabel?: string;
  readonly ctaHref?: string;
}

const ITEMS: readonly NewsroomItem[] = [
  {
    slug: "cloud-7t-market-enthusiasts-viral-demand-2030",
    kicker: "Scenario",
    category: "note",
    title: "Cloud will be a $2.4T market by 2030.",
    deck: "Enthusiasts will drive demand starting in 2027.",
    date: "18 June 2026",
    publishedAt: "2026-06-18",
    author: { name: "Shovon Hasan", role: "Founder & CEO", avatar: "/people/shovon-hasan.jpg" },
    lead: [
      "Goldman Sachs forecasts Cloud TAM will grow to [$2 trillion by 2030](https://www.goldmansachs.com/insights/articles/cloud-revenues-poised-to-reach-2-trillion-by-2030-amid-ai-rollout), up from [$723B in 2025](https://www.gartner.com/en/newsroom/press-releases/2024-11-19-gartner-forecasts-worldwide-public-cloud-end-user-spending-to-total-723-billion-dollars-in-2025). We find $360B of TAM by 2030, driven by latent demand from enthusiasts beginning in 2027.",
      "[Guardian](https://github.com/guardian-intelligence/guardian) reduces the cost and complexity of running a software company. By 2030, 10% of college-educated Americans will run a personal software company — a new generation of small business owners, a TAM of ~$360 billion.",
      "Compute providers, like Latitude et al., are poised to capture the lion's share of enthusiast demand. A $360B opportunity over the next 4 years.",
    ],
    body: [
      "SEATTLE, WA — Today, Guardian Intelligence LLC announces its forecast that direct-to-consumer bare metal hosting becomes a trillion-dollar market by 2030, as coding agents turn software creation from an enterprise discipline into a consumer one. When everyone runs a personal software company, the premium layer of that demand resolves to dedicated hardware.",
      "The coding agent will reshape economies into a ~$7 trillion market by 2040 — the scale of today's mobile economy — and become as essential to daily life as owning a car. Bare metal hardware will be the premium tier, analogous to a flagship phone.",
      "READ: A letter from the founder: [The Coding Agent is the Next Smartphone](/letters/the-coding-agent-is-the-next-smartphone)",
    ],
    infographic: "guardian-up",
    interactive: {
      kind: "bare-metal-tam-playground",
      defaults: {
        currentCloudTamBillion: 723,
        cloudTam2030Billion: 2000,
        currentEnthusiastDemandBillion: DEFAULT_CURRENT_ENTHUSIAST_DEMAND_BILLION,
        segmentCagrPct: DEFAULT_SEGMENT_CAGR_PCT,
      },
    },
  },
  {
    slug: "guardian-intelligence-announces-postflight-private-beta",
    kicker: "Announcement",
    category: "announcement",
    title: "Guardian Intelligence Inc. announces private beta of Postflight.",
    deck: "Postflight is the world's fastest Agent-Native Cloud, fully open-source and built for durable developer and agent environments.",
    date: "1 June 2026",
    publishedAt: "2026-06-01",
    author: { name: "Shovon Hasan", role: "Founder & CEO", avatar: "/people/shovon-hasan.jpg" },
    body: [
      "Seattle, Washington -- Guardian Intelligence Inc. today announced the private beta of Postflight, the world's fastest Agent-Native Cloud. Postflight is a fully open-source cloud platform designed for agent-native software teams, pairing bare-metal performance with persistent developer environments so humans and coding agents can build, test, and deploy from the same durable system state.",
      "The private beta will focus on teams working at the edge of AI-assisted software delivery, where fast feedback loops, reproducible deployments, and secure-by-default infrastructure are product requirements rather than tooling details. Guardian Intelligence will share more availability details as the beta expands.",
    ],
    ctaLabel: "Read the announcement",
  },
  {
    slug: "brand-system-shipped",
    kicker: "Milestone",
    category: "milestone",
    title: "Three rooms, one house.",
    deck: "Workshop, Newsroom, and Letters are now the three treatments that carry Guardian across every surface — and the design page walks the whole system.",
    date: "19 April 2026",
    publishedAt: "2026-04-19",
    author: { name: "Shovon Hasan", role: "Founder & CEO", avatar: "/people/shovon-hasan.jpg" },
    body: [
      "The brand model collapsed to three rooms. Each one paints its own ground, binds its own display font, and carries its own accent. A single data attribute on the nearest ancestor resolves every token downstream, which means the same page shell renders three different registers depending on where it sits in the site.",
      "Workshop is the everyday register — the product chrome, the marketing site, the console. Newsroom is the broadcast register — this room. Letters is the editorial register — where long-form lives. The rooms share a wordmark, a wings mark, and a single typographic idea; everything else is the treatment's choice.",
      "The /design page walks the whole system. Each room has a specimen card; step into the room and you see it inhabited.",
    ],
    ctaLabel: "See the rooms",
    ctaHref: "/design",
  },
  {
    slug: "letters-is-live",
    kicker: "Milestone",
    category: "milestone",
    title: "Letters is live.",
    deck: "The editorial register shipped with a seeded essay. Letters is the place where we explain the why.",
    date: "12 April 2026",
    publishedAt: "2026-04-12",
    author: { name: "Shovon Hasan", role: "Founder & CEO", avatar: "/people/shovon-hasan.jpg" },
    body: [
      "Letters ships on Paper. Ink type, Fraunces display, Bordeaux reserved for the single pull-quote rule — nothing else on the page is allowed that colour. The register is periodical: we publish when we have something to say, not on a calendar.",
      "The first letter is out today. The index lives at /letters and reads like a gazette.",
    ],
    ctaLabel: "Open Letters",
    ctaHref: "/letters",
  },
  {
    slug: "observability-in-public",
    kicker: "Note",
    category: "note",
    title: "Observability, in public.",
    deck: "Every route on this site emits a trace that lands in our own ClickHouse, on the same pipeline our customers use. We publish what we see there.",
    date: "5 April 2026",
    publishedAt: "2026-04-05",
    author: { name: "Shovon Hasan", role: "Founder & CEO", avatar: "/people/shovon-hasan.jpg" },
    body: [
      "The site runs on the same telemetry surface as the rest of the platform. Every route mount, every card click, every subscribe submit is a span. The spans land in ClickHouse. The same evidence we ask of our own services we ask of our own website.",
      "The point is not that we have telemetry. The point is that when we say a feature shipped and works, a queryable artifact says so.",
    ],
  },
];

// Reverse-chronological by convention. Returned read-only so call sites can't
// mutate the module-level array.
export function sortedNewsroomItems(): readonly NewsroomItem[] {
  return ITEMS;
}

export function currentNewsroomItem(): NewsroomItem | undefined {
  return ITEMS[0];
}

export function newsroomItemBySlug(slug: string): NewsroomItem | undefined {
  return ITEMS.find((item) => item.slug === slug);
}

// The default deep link for a bulletin is its article route. Callers can
// override via ctaHref for the rare bulletin that tees up a non-article
// destination (a design page, an external launch page, etc.).
export function newsroomCtaHref(item: NewsroomItem): string {
  return item.ctaHref ?? `/news/${item.slug}`;
}

export function newsroomCtaLabel(item: NewsroomItem): string {
  return item.ctaLabel ?? "Read the bulletin";
}

export const NEWSROOM_META = {
  title: "News — Guardian",
  description: "News, milestones, and public notes from Guardian Intelligence.",
  siteURL: "https://guardianintelligence.org",
} as const;

export const CATEGORY_LABELS: Record<NewsroomCategory, string> = {
  announcement: "Announcements",
  milestone: "Milestones",
  note: "Notes",
};

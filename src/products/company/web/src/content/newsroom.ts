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
  readonly body: readonly string[];
  readonly interactive?: NewsroomInteractive;
  readonly ctaLabel?: string;
  readonly ctaHref?: string;
}

const ITEMS: readonly NewsroomItem[] = [
  {
    slug: "direct-to-consumer-bare-metal-hosting-1t-market-2030",
    kicker: "Scenario",
    category: "note",
    title: "Direct-to-consumer bare metal hosting will be a $1T market by 2030.",
    deck: "Cloud growth alone makes hosted bare metal much larger. The trillion-dollar case starts when consumer builders and new software companies stop treating local hardware as the default.",
    date: "18 June 2026",
    publishedAt: "2026-06-18",
    author: { name: "Shovon Hasan", role: "Founder & CEO", avatar: "/people/shovon-hasan.jpg" },
    body: [
      "This is a scenario, not a forecast. The chart below starts from one observable number — today's cloud TAM — and projects a single question forward to 2030: how big does direct-to-consumer bare metal hosting get if it stops being a niche server category and becomes the default hardware layer for serious software work? Four levers drive the model, and every one of them is yours to move: today's cloud TAM, the 2030 cloud TAM, and the growth of the two markets we think are mispriced — the hobbyist and the software factory.",
      "Start with cloud. Cloud spend has compounded for fifteen years and shows no sign of stopping; we index the window to a base near $723B today growing toward $2.5T by 2030. That growth alone is the floor of the argument. Bare metal does not have to take share from cloud to grow — it rides the same demand curve, because the workloads underneath are the same workloads.",
      "So we treat bare metal not as a separate, sleepy server market but as a fixed share of cloud TAM. Hold that share constant — a $100B baseline indexed to cloud — and the bare-metal line alone clears a third of a trillion dollars by 2030 without a single behavior changing. That is the unglamorous part of the thesis, and it is the part we are most confident in.",
      "The first behavior change is the hobbyist. Building a personal machine for serious AI or heavy local development gets less attractive every quarter: the hardware is expensive, it sits idle, and it is obsolete before it is paid off. That demand does not vanish — it moves to hosted bare metal that someone else racks, cools, and amortizes. We model it as incremental demand layered on the cloud-indexed line, not as the core market, which is why its lever is measured in single-digit percentages.",
      "The second behavior change is the one that bends the curve: new software companies that never buy a rack and never rent a hyperscaler VM, because the default substrate for a company-as-code is hosted bare metal from day one. We call this the software factory. When founding a company means provisioning a computation substrate the way you provision a repository, the bare-metal market is no longer indexed to cloud — it is a multiple of it. Default behavior is a step function, and the step lands inside this window.",
      "Put the three together and the model crosses a trillion dollars by 2030 under the defaults: the cloud-indexed floor, plus hobbyist displacement, plus new companies defaulting to bare metal. Move the levers and argue with us — the assumptions are all on the page. The claim is narrow and deliberately so. Direct-to-consumer bare metal becomes the default hardware layer for serious software work, and that makes it a trillion-dollar category.",
    ],
    interactive: {
      kind: "bare-metal-tam-playground",
      defaults: {
        currentCloudTamBillion: 723,
        cloudTam2030Billion: 2500,
        hobbyistGrowthPct: 7,
        softwareFactoryGrowthPct: 200,
        currentBareMetalTamBillion: 100,
        startQuarter: "2026Q3",
        endQuarter: "2030Q4",
      },
    },
  },
  {
    slug: "guardian-intelligence-announces-verself-private-beta",
    kicker: "Announcement",
    category: "announcement",
    title: "Guardian Intelligence Inc. announces private beta of Verself.",
    deck: "Verself is the world's fastest Agent-Native Cloud, fully open-source and built for durable developer and agent environments.",
    date: "1 June 2026",
    publishedAt: "2026-06-01",
    author: { name: "Shovon Hasan", role: "Founder & CEO", avatar: "/people/shovon-hasan.jpg" },
    body: [
      "Seattle, Washington -- Guardian Intelligence Inc. today announced the private beta of Verself, the world's fastest Agent-Native Cloud. Verself is a fully open-source cloud platform designed for agent-native software teams, pairing bare-metal performance with persistent developer environments so humans and coding agents can build, test, and deploy from the same durable system state.",
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

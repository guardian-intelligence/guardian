import type { ReactNode } from "react";
import { LOGO_SOURCES, SAMPLE_SOURCES, ValueChain } from "./ValueChain";
import { Lockup } from "./Brand";
import { Title } from "./Title";
import { GrowthChart, type RunRateSeries } from "./RunRates";
import { Month1Flow } from "./Month1Flow";
import { Month2Flow } from "./Month2Flow";
import { Buyers } from "./Buyers";
import { DroneMarginFigure } from "./DroneMargin";
import { LastMile } from "./LastMile";
import { PhasePath } from "./PhasePath";
import { ASSUMPTIONS } from "~/model/roadmap";
import { useKeyboardNav } from "./keyboardNav";
import { ICONS, LoopFlow } from "./LoopFlow";
import { ROADMAP_SOURCES } from "./roadmapSources";
const SLIDES = [
  { id: "title", title: "Anveio" },
  { id: "who", title: "Who We Are" },
  { id: "cover", title: "What we do" },
  { id: "market", title: "Why Now" },
  { id: "why", title: "Why Bangladesh" },
  { id: "roadmap", title: "Where this is going" },
  { id: "p0-m1", title: "Month 1" },
  { id: "p0-m2", title: "Month 2" },
  { id: "p0-m3", title: "Month 3" },
  { id: "p0-m4", title: "Month 4" },
  { id: "numbers", title: "By the Numbers" },
  { id: "sources", title: "Sources" },
  { id: "sources-roadmap", title: "Sources: Roadmap" },
] as const;

type SlideId = (typeof SLIDES)[number]["id"];
const SLIDE_IDS = SLIDES.map((s) => s.id);

function Slide({
  id,
  note,
  children,
}: {
  id: SlideId;
  note?: string | undefined;
  children: ReactNode;
}) {
  const slide = SLIDES.find((s) => s.id === id)!;
  return (
    <section id={slide.id} className="slide" aria-label={slide.title}>
      <div className="slide-frame">
        <div className="slide-inner">
          <h2 className="slide-heading">{slide.title}</h2>
          {children}
          <footer className="slide-foot">
            <Lockup />
            {note && <span>{note}</span>}
          </footer>
        </div>
      </div>
    </section>
  );
}

function Cover() {
  return (
    <Slide id="cover" note="Photos: Wikimedia Commons">
      <div className="cover">
        <p className="pitch-lead">
          We pay farms in Bangladesh to record drone video, then we enrich it and format it, and
          then license it to AI companies.
        </p>
        <div className="cover-figure">
          <ValueChain />
        </div>
        <div className="cover-margin">
          <DroneMarginFigure width={200} />
          <ul className="cover-margin-notes">
            <li>
              Licenses renew yearly, so the same hour of footage is sold per customer per year.
            </li>
            <li>$40/hr for exclusive contracts.</li>
          </ul>
        </div>
      </div>
    </Slide>
  );
}

const WHY = [
  "AI is accelerating innovation. Data is the bottleneck. Later, deployment will be.",
  "Bangladesh has a malleable bureaucracy, the highest population density of any large country, and a US-friendly government.",
  "Bangladesh is rich in farms and fertile, low-cost land.",
  "Natural moat: first company to deploy data collection infrastructure at scale will win. Small economy = winner-take-all.",
  "Long term: become default data partner for buyers of high quality physical world-data in the region.",
] as const;

function WhyBangladesh() {
  return (
    <Slide
      id="why"
      note="Boundaries: geoBoundaries (BBS, OCHA) · Rivers: © OpenStreetMap contributors"
    >
      <div className="why">
        <div className="why-text">
          <ul className="why-points">
            {WHY.map((point) => (
              <li key={point}>{point}</li>
            ))}
          </ul>
        </div>
        <LastMile />
      </div>
    </Slide>
  );
}

// How we become the most trusted source: drone video, paired with sensors
// and evals on the same plots, then more farms.
const DATA_PLAN = [
  "Drone video",
  "Physical sensors (salinity, temperature, moisture)",
  "Evals (AgricultureBench 1.0, RL Envs)",
  // BBS Agriculture Census 2019: 16.88M farm households.
  "Scale horizontally: 2 → 16.9M farms",
] as const;

// The deployment work, from the AI Deployment Engine plan.
const DEPLOYMENTS = [
  "Last-mile delivery of breakthrough medicine",
  "Specialized vehicles that make supplying the most remote villages economical",
  "Building materials and construction standards tuned to Bangladesh’s climate and geography",
  "Safer roads and self-driving public transit",
  "Pollution removal, sanitation, garbage collection and water treatment",
  "Cheap lab-grown meat to fight malnutrition",
] as const;

function Roadmap() {
  return (
    <Slide id="roadmap" note="BBS Agriculture Census 2019">
      <div className="where">
        <ul className="where-points">
          <li>
            First, we become the world’s most trusted source of AI data in Bangladesh. (Q4 2028)
            <ul>
              {DATA_PLAN.map((d) => (
                <li key={d}>{d}</li>
              ))}
            </ul>
          </li>
          <li>
            Second, we become the deployment vehicle for all AI-powered innovation:
            <ul>
              {DEPLOYMENTS.map((d) => (
                <li key={d}>{d}</li>
              ))}
            </ul>
          </li>
        </ul>
        <div className="where-plan">
          <PhasePath />
        </div>
      </div>
    </Slide>
  );
}

// The roadmap chart's assumptions, read at its end.
function Numbers() {
  return (
    <Slide id="numbers" note="Epoch, Jan 2026">
      <ul className="numbers">
        <li>
          Evals: ${ASSUMPTIONS.evalQuarter / 1000}k/quarter per contract (Phase 0 ={" "}
          {ASSUMPTIONS.evalsPhase0} contract, {ASSUMPTIONS.evalsEnd} by {ASSUMPTIONS.endYear})
        </li>
        <li>
          Drone video: {ASSUMPTIONS.plots} farms × {ASSUMPTIONS.hrsPerDay} hrs/day at $
          {ASSUMPTIONS.rate}/hr, licensed to {ASSUMPTIONS.buyersPhase0} labs, rising to{" "}
          {ASSUMPTIONS.buyersEnd} by {ASSUMPTIONS.endYear}; licenses renew yearly
        </li>
        <li>
          Price per hour rises {ASSUMPTIONS.historyPremium * 100}% for each year of history on the
          plot: more history gives new footage more context
        </li>
        <li>
          Partner farms: {ASSUMPTIONS.onboardingStart}–{ASSUMPTIONS.onboardingEarly} new farms a
          month from {ASSUMPTIONS.onboardingFrom}, rising to {ASSUMPTIONS.onboardingFull} a month by{" "}
          {ASSUMPTIONS.onboardingFullFrom}
        </li>
      </ul>
    </Slide>
  );
}

// Month 1 for the family on the farm: how drone video reaches Anveio.
function Month1() {
  return (
    <Slide id="p0-m1">
      <div className="month-1">
        <p className="phase-lead">Goal: Collect at least 20 hours of high quality drone footage.</p>
        <Month1Flow />
      </div>
    </Slide>
  );
}

// Month 2: label the footage and take it to brokers.
function Month2() {
  return (
    <Slide id="p0-m2">
      <div className="month-1 month-2">
        <p className="phase-lead">Goal: Go to market, get one buyer commitment.</p>
        <Month2Flow />
        <Buyers />
      </div>
    </Slide>
  );
}

// Month 3: hire drone operators; license fees pay for the next hires.
function Month3() {
  return (
    <Slide id="p0-m3">
      <div className="month-1 month-2">
        <p className="phase-lead">Goal: Record 10hrs/day/farm of drone video.</p>
        <LoopFlow
          id="m3"
          start="Hire operators"
          steps={[
            { label: "Record more hours a day", icon: ICONS.video },
            { label: "Improve data pipeline", icon: ICONS.pipeline },
            { label: "License data", icon: ICONS.money },
          ]}
          loopTo="start"
          loop={{ text: "Reinvest", icon: ICONS.money }}
          label="Anveio hires drone operators, records more hours a day, improves the data pipeline and licenses the data; the license fees pay for more operators."
        />
      </div>
    </Slide>
  );
}

// Month 4: turn the dataset into AgricultureBench, sold through buyers until
// we go direct to AI labs.
function Month4() {
  return (
    <Slide id="p0-m4">
      <div className="month-1 month-2">
        <div>
          <p className="phase-lead">Goal: Release AgricultureBench-1.0</p>
          <p className="month-sub">Eval = Benchmark + RL Env</p>
        </div>
        <LoopFlow
          id="m4"
          start="Create eval"
          steps={[
            { label: "Sell to buyers", icon: ICONS.money },
            { label: "Collect more raw video", icon: ICONS.video },
            { label: "Improve eval", anveio: true },
          ]}
          label="Anveio creates an eval (a benchmark plus an RL environment); then, on repeat: sell it to buyers, collect more raw video, improve the eval."
        />
      </div>
    </Slide>
  );
}

// The date the deck's market figures are current to.
const TODAY = "2026-10-08";
// Both market charts start here so their time axes line up.
const FROM = "2024-09";

// Publicly announced run rates (mostly gross annualized; company claims).
// Dated points only; see Sources.
const RUN_RATES: RunRateSeries[] = [
  {
    name: "micro1",
    // 2022 (approx).
    founded: "2022-07",
    logo: "/deck/logos/micro1.png",
    logoAt: { dx: 4, dy: -15 },
    points: [
      { date: "2025-01", value: 7e6 },
      { date: "2025-09", value: 50e6 },
      { date: "2025-12", value: 100e6 },
      { date: "2026-08", value: 500e6 },
    ],
  },
  {
    name: "Mercor",
    founded: "2023-01",
    logo: "/deck/logos/mercor.png",
    logoAt: { dx: 13, dy: 0 },
    points: [
      { date: "2025-02", value: 75e6 },
      { date: "2025-03", value: 100e6 },
      { date: "2025-09", value: 450e6 },
      { date: "2026-06", value: 2e9 },
    ],
  },
  {
    name: "Snorkel",
    // Expert Data-as-a-Service launch; the company dates from 2019.
    founded: "2025-05",
    logo: "/deck/logos/snorkel.png",
    logoAt: { dx: 13, dy: -1 },
    points: [
      { date: "2025-09", value: 20e6 },
      { date: "2026-09", value: 350e6 },
    ],
  },
  {
    name: "Handshake",
    // Handshake AI launch; the company dates from 2014.
    founded: "2025-01",
    logo: "/deck/logos/handshake.png",
    logoAt: { dx: 11, dy: -14 },
    points: [
      { date: "2025-10", value: 100e6 },
      { date: "2026-01", value: 550e6 },
      { date: "2026-04", value: 1e9 },
    ],
  },
];

// Cumulative capital raised from announced rounds (company-reported totals
// where rounds don't sum). See Sources.
const RAISES: RunRateSeries[] = [
  {
    name: "Mecka",
    // 2024 (approx).
    founded: "2024-07",
    logo: "/deck/logos/mecka.png",
    logoAt: { dx: 14, dy: 0 },
    points: [
      { date: "2025-11", value: 25e6 },
      { date: "2026-06", value: 60e6 },
      { date: "2026-10", value: 120e6 },
    ],
  },
  {
    name: "Protege",
    // Early 2024 (approx).
    founded: "2024-02",
    logo: "/deck/logos/protege.png",
    logoAt: { dx: 12, dy: -7 },
    points: [
      { date: "2024-09", value: 10e6 },
      { date: "2025-08", value: 35e6 },
      { date: "2026-01", value: 65e6 },
    ],
  },
  {
    name: "Luel",
    // 2025 (approx; YC W26, so late in the year).
    founded: "2025-12",
    logo: "/deck/logos/luel.png",
    logoAt: { dx: 13, dy: -4 },
    points: [{ date: "2026-05", value: 31.2e6 }],
  },
  {
    name: "Build AI",
    // Undisclosed; first seen raising in Sep 2025.
    founded: "2025-01",
    logo: "/deck/logos/buildai.png",
    logoAt: { dx: 9, dy: -14 },
    points: [
      { date: "2025-09", value: 5e6 },
      { date: "2025-12", value: 15e6 },
    ],
  },
  {
    name: "Kled",
    // 2025 (approx).
    founded: "2025-01",
    logo: "/deck/logos/kled.png",
    logoAt: { dx: 13, dy: -2 },
    points: [
      { date: "2026-03", value: 9e6 },
      { date: "2026-06", value: 14e6 },
    ],
  },
];

const MARKET_SOURCES = [
  {
    label: "TechCrunch, micro1 raises at $500M valuation, $7M to $50M ARR (Sep 2025)",
    url: "https://techcrunch.com/2025/09/12/micro1-a-competitor-to-scale-ai-raises-funds-at-500m-valuation/",
  },
  {
    label: "TechCrunch, micro1 crosses $100M ARR (Dec 2025)",
    url: "https://techcrunch.com/2025/12/04/micro1-a-scale-ai-competitor-touts-crossing-100m-arr/",
  },
  {
    label: "TechCrunch, micro1 reaches $500M gross run rate (Aug 2026)",
    url: "https://techcrunch.com/2026/08/20/ai-data-startup-micro1-reaches-500m-gross-run-rate-amid-ai-training-boom/",
  },
  {
    label: "TechCrunch, Mercor run rate history (Sep 2025)",
    url: "https://techcrunch.com/?p=3044695",
  },
  {
    label: "TechCrunch, AI startups growing revenue faster (Mercor $2B, Jul 2026)",
    url: "https://techcrunch.com/2026/07/08/these-ai-startups-are-growing-revenue-at-faster-and-faster-rates/",
  },
  {
    label: "Snorkel AI press release, run rate past $350M (Sep 2026)",
    url: "https://snorkel.ai/press/snorkel-ai-valued-at-3-5-billion-amid-surging-demand-for-complex-ai-training-data-2/",
  },
  {
    label: "Upstarts Media via Techmeme, Handshake AI $100M run rate (Oct 2025)",
    url: "https://thenote.app/post/en/handshake-which-connects-experts-with-ai-labs-to-help-train-llms-says-it-hit-3ijbk3l5p6",
  },
  {
    label: "Dealroom citing The Information, Handshake AI nears $1B (Apr 2026)",
    url: "https://dealroom.co/news/127345-handshakes-arr-crosses-1b-as-ai-training-revenue-surges/",
  },
  {
    label: "BetaKit, Mecka AI $60M Series B led by Sequoia (Oct 2026)",
    url: "https://betakit.com/mecka-ai-reveals-60-million-usd-series-b-round-backed-by-sequoia-nvidia/",
  },
  {
    label: "Fortune via RuntimeWire, Mecka AI raises $60M (Jun 2026)",
    url: "https://runtimewire.com/article/mecka-ai-raised-60m-to-train-robots-on-human-motion-data",
  },
  {
    label: "Protege, $10M seed (Sep 2024)",
    url: "https://withprotege.substack.com/p/protege-raises-10-million-and-launches",
  },
  {
    label: "CDO Magazine, Protege $25M Series A (Aug 2025)",
    url: "https://www.cdomagazine.tech/others/protege-announces-25-million-series-a-to-expand-ai-training-data-platform",
  },
  {
    label: "Lowenstein, Protege $30M Series A-1 led by a16z (Jan 2026)",
    url: "https://www.lowenstein.com/news-insights/firm-news/lowenstein-represents-protege-in-30m-series-a-funding-round-lead-by-a16z",
  },
  {
    label: "Lightspeed, Luel $31.2M (May 2026)",
    url: "https://lsvp.com/stories/our-investment-in-luel-the-marketplace-for-multimodal-ai-training-data/",
  },
  {
    label: "Humanoids Daily, Build AI $15M (Dec 2025)",
    url: "https://www.humanoidsdaily.com/news/build-ai-scales-to-100-000-hours-as-data-scaling-becomes-robotics-new-frontier",
  },
  {
    label: "OurCryptoTalk, Kled $5.5M seed (Mar 2026)",
    url: "https://ourcryptotalk.com/news/kled-ai-raises-5-5m-to-build-human-data-marketplace",
  },
  {
    label: "Phemex, Kled $3M from The Data Foundation (Jun 2026)",
    url: "https://phemex.com/news/article/kled-ai-secures-3-million-investment-from-the-data-foundation-91054",
  },
  {
    label: "Gartner, worldwide AI spending forecast, AI Data segment (May 2026)",
    url: "https://gcom.pdo.aws.gartner.com/en/newsroom/press-releases/2026-05-19-gartner-forecasts-worldwide-ai-spending-to-grow-47-percent-in-2026",
  },
] as const;

function Market() {
  return (
    <Slide id="market" note="Sources: Gartner, TechCrunch, The Information, company releases">
      <div className="market">
        <ul className="market-points">
          <li>
            AI data industry is seeing unprecedented growth.
            <ul>
              {/* Largest first-to-latest multiple charted: micro1, $7M (Jan 2025) to $500M (Aug 2026). */}
              <li>Seller revenue grew up to 71× between 2025 and 2026.</li>
              {/* Gartner, May 2026 "AI Data" spend: $0.83B (2025) → $3.1B (2026) → $6.5B (2027). */}
              <li>Buyer spending is expected to grow 7.8× between 2025 and 2027.</li>
            </ul>
          </li>
          <li>
            Latest models are powerful enough to create overwhelming comparative advantage against
            non-AI incumbents in Bangladesh.
          </li>
          <li>
            Historical farming data becomes more valuable over time and makes AI better at
            agriculture, business, world-modeling, and more.
          </li>
        </ul>
        <div className="market-charts">
          <GrowthChart
            heading="Revenue"
            title="Data Company Revenues"
            series={RUN_RATES}
            from={FROM}
            asOf={TODAY}
            multiple
          />
          <GrowthChart
            heading="Funding"
            title="Data Company Raises"
            series={RAISES}
            from={FROM}
            asOf={TODAY}
          />
        </div>
      </div>
    </Slide>
  );
}

type Logo = { src: string; alt: string };
// Hand placement in the logo box (px): deliberately uneven, as if dragged in.
type Placed = Logo & { x: number; y: number; h: number };

type Member = {
  name: string;
  role: string;
  photo: string | null;
  url: string | null;
  // Plain bullet points.
  bio: readonly string[];
  logos: readonly Placed[];
};

const FOUNDER: Member = {
  name: "Shovon Hasan",
  role: "Founder, CEO",
  photo: "/deck/team/shovon.jpg",
  url: "https://x.com/anveio",
  bio: [
    "10+ YoE, AWS (EC2, Bedrock) & Patreon (Payments)",
    "Seattle-based, born in Bangladesh.",
    "Anveio is a US‑based C‑corp with a subsidiary in Bangladesh.",
    "Mission: Turn Bangladesh into the world’s most efficient source of AI data.",
  ],
  logos: [
    { src: "/deck/logos/aws.svg", alt: "AWS", x: 6, y: 18, h: 52 },
    { src: "/deck/logos/patreon-mark.svg", alt: "Patreon", x: 148, y: 4, h: 58 },
    { src: "/deck/logos/madhive-mark.png", alt: "MadHive", x: 214, y: 78, h: 50 },
    { src: "/deck/logos/emory-mark.svg", alt: "Emory University", x: 52, y: 92, h: 64 },
  ],
};

// Advisors carry a placeholder avatar and their name; companies carry their
// full logo, which names them, and what they've put in.
const PARTNERS: readonly {
  name: string;
  url: string | null;
  // Full logo, at a height that gives every logo the same ink area
  // (about 3,300 px²), so a wide wordmark and a squarer mark read alike.
  logo?: Logo & { h: number };
  detail: string;
}[] = [
  {
    name: "Dr. Tazia Sardar, DPT",
    url: "https://www.linkedin.com/in/dr-tazia-sardar-dpt-2751b1a8/",
    detail: "Advisor",
  },
  { name: "Dr. Md Abdullah Yousuf, DO", url: null, detail: "Advisor" },
  {
    name: "OpenAI",
    url: null,
    logo: { src: "/deck/logos/openai-wordmark.svg", alt: "OpenAI", h: 30 },
    detail: "$1,600 · Daybreak Blue",
  },
  {
    name: "Anthropic",
    url: null,
    logo: { src: "/deck/logos/anthropic-wordmark.svg", alt: "Anthropic", h: 19 },
    detail: "$2,500",
  },
];

// LinkedIn-style placeholder for people without a photo.
function BlankAvatar() {
  return (
    <svg viewBox="0 0 100 100" className="avatar" aria-hidden="true">
      <rect width="100" height="100" className="avatar-bg" />
      <circle cx="50" cy="38" r="18" className="avatar-fg" />
      <path d="M14 100 C14 72 30 62 50 62 C70 62 86 72 86 100 Z" className="avatar-fg" />
    </svg>
  );
}

const Linked = ({ name, url }: { name: string; url: string | null }) =>
  url ? (
    <a href={url} target="_blank" rel="noreferrer">
      {name}
    </a>
  ) : (
    name
  );

// The founder and plain bullet points across the top; the partners below.
function WhoWeAre() {
  const f = FOUNDER;
  return (
    <Slide id="who">
      <div className="who">
        <section className="who-founder">
          <div className="team-who">
            {f.photo ? <img className="avatar" src={f.photo} alt={f.name} /> : <BlankAvatar />}
            <strong>
              <Linked name={f.name} url={f.url} />
            </strong>
            <span className="team-role">{f.role}</span>
          </div>
          <ul className="team-bio">
            {f.bio.map((line) => (
              <li key={line}>{line}</li>
            ))}
          </ul>
          <div className="team-logos">
            {f.logos.map((logo) => (
              <img
                key={logo.src}
                className="team-logo"
                src={logo.src}
                alt={logo.alt}
                style={{ left: logo.x, top: logo.y, height: logo.h }}
              />
            ))}
          </div>
        </section>
        <section className="who-advisors">
          <h3 className="who-partners-heading">Partners</h3>
          <ul>
            {PARTNERS.map((p) => (
              <li key={p.name}>
                {p.logo ? (
                  <span className="who-partner-logo">
                    <img src={p.logo.src} alt={p.logo.alt} style={{ height: p.logo.h }} />
                  </span>
                ) : (
                  <>
                    <BlankAvatar />
                    <strong>
                      <Linked name={p.name} url={p.url} />
                    </strong>
                  </>
                )}
                <span>{p.detail}</span>
              </li>
            ))}
          </ul>
        </section>
      </div>
    </Slide>
  );
}

type SourceGroup = {
  name: string;
  items: readonly { label: string; url: string }[];
  columns?: number;
};

// A slide holding a single group is titled after it, so its eyebrow is
// dropped.
function Sources({
  id,
  groups,
  note,
}: {
  id: SlideId;
  groups: readonly SourceGroup[];
  note?: string;
}) {
  return (
    <Slide id={id} note={note}>
      <div className="sources">
        {groups.map((g) => (
          <section key={g.name}>
            {groups.length > 1 && <h2 className="sources-head">{g.name}</h2>}
            <ol style={{ columns: g.columns ?? 2 }}>
              {g.items.map((src) => (
                <li key={src.url + src.label}>
                  <a href={src.url} target="_blank" rel="noreferrer">
                    {src.label}
                  </a>
                </li>
              ))}
            </ol>
          </section>
        ))}
      </div>
    </Slide>
  );
}

// Layout is pure CSS (see .deck in app.css), so the server-rendered page is
// already final; script only adds keyboard navigation.
export function Deck() {
  useKeyboardNav(SLIDE_IDS);
  return (
    <>
      <main className="deck">
        <Title />
        <WhoWeAre />
        <Cover />
        <Market />
        <WhyBangladesh />
        <Roadmap />
        <Month1 />
        <Month2 />
        <Month3 />
        <Month4 />
        <Numbers />
        <Sources
          id="sources"
          groups={[
            { name: "Market", items: MARKET_SOURCES },
            { name: "Photos", items: SAMPLE_SOURCES },
            { name: "Logos", items: LOGO_SOURCES, columns: 3 },
          ]}
        />
        <Sources id="sources-roadmap" groups={[{ name: "Roadmap", items: ROADMAP_SOURCES }]} />
      </main>
    </>
  );
}

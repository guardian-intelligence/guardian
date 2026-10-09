import { useEffect, type ReactNode } from "react";
import { northStar, scenarioInputs } from "~/model/model";
import { MarginChart, ScalingLaws } from "./charts";
import { LOGO_SOURCES, SAMPLE_SOURCES, ValueChain } from "./ValueChain";
import { Lockup } from "./Brand";
import { GrowthChart, type RunRateSeries } from "./RunRates";
import { PhasePath } from "./PhasePath";
import { LastMile } from "./LastMile";

const INPUTS = scenarioInputs("base");
const STAR = northStar(INPUTS);
const SLIDES = [
  { id: "title", title: "Anveio" },
  { id: "who", title: "Who We Are" },
  { id: "cover", title: "What we do" },
  { id: "moats", title: "Why us" },
  { id: "market", title: "Market" },
  { id: "why", title: "Why Bangladesh" },
  { id: "economics", title: "Phase 1 Economics" },
  { id: "path", title: "Path to $1B" },
  { id: "vision", title: "Where This Goes" },
  { id: "ask", title: "The Ask" },
  { id: "sources", title: "Sources" },
] as const;

type SlideId = (typeof SLIDES)[number]["id"];

function Slide({ id, note, children }: { id: SlideId; note?: string; children: ReactNode }) {
  const slide = SLIDES.find((s) => s.id === id)!;
  return (
    <section id={slide.id} className="slide" aria-label={slide.title}>
      <div className="slide-frame">
        <div className="slide-inner">
          <header className="slide-head">
            <Lockup />
          </header>
          <h2 className="slide-heading">{slide.title}</h2>
          {children}
          <footer className="slide-foot">
            <span>Anveio</span>
            {note && <span>{note}</span>}
          </footer>
        </div>
      </div>
    </section>
  );
}

const MARGINS = [
  { label: "Driving", cost: 4, price: 8 },
  { label: "Egocentric", cost: 6, price: 15 },
  { label: "Drone", cost: 8, price: 25 },
] as const;

// The opening slide: name, mission and strategy, set full-bleed in ink.
function Title() {
  return (
    <section id="title" className="slide" aria-label="Anveio">
      <div className="slide-frame">
        <div className="slide-inner title-slide">
          <header className="slide-head">
            <Lockup onInk />
          </header>
          <dl className="title-body">
            <dt>Mission</dt>
            <dd className="title-mission">
              Deploy AGI for the benefit of all humans in Bangladesh.
            </dd>
            <dt>Strategy</dt>
            <dd className="title-strategy">
              Turn Bangladesh into the world’s most efficient source of AI data and the best place
              to deploy AI‑powered innovation.
            </dd>
          </dl>
          <footer className="title-foot">
            <span>anveio.com</span>
            <span>Investor preview · October 2026</span>
          </footer>
        </div>
      </div>
    </section>
  );
}

function Cover() {
  return (
    <Slide id="cover" note="Photos: Wikimedia Commons, Build AI">
      <div className="cover">
        <p className="pitch-lead">
          We collect high quality video in Bangladesh and license access to buyers.
        </p>
        <div className="cover-figure">
          <ValueChain />
        </div>
        <p className="cover-foot">
          We work closely with our customers to rapidly translate unmet demand into low-cost work
          contracts with our partners who subcontract out data collection.
        </p>
      </div>
    </Slide>
  );
}

const WHY = [
  "AI is accelerating innovation. Deployment is now the bottleneck.",
  "Bangladesh has a malleable bureaucracy, the highest population density of any large country, and a US-friendly government.",
  "Deploying here is hard: a river delta, monsoons, Bangla, local politics and culture. That difficulty is our moat.",
  "A small economy is winner-take-all.",
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
          <p className="why-therefore">
            So we make Bangladesh the world’s best place to deploy AI‑powered innovation, and become
            the obvious partner for doing it.
          </p>
        </div>
        <LastMile />
      </div>
    </Slide>
  );
}

function Economics() {
  return (
    <Slide id="economics">
      <div className="economics">
        <MarginChart rows={MARGINS} />
        <ScalingLaws star={STAR} />
      </div>
    </Slide>
  );
}

function PathToBillion() {
  return (
    <Slide id="path" note="Gross ARR · Illustrative path; Phase 1 is modelled bottom-up">
      <PhasePath />
    </Slide>
  );
}

const VISION = [
  "Become the largest company in Bangladesh.",
  "Automate all agriculture and manufacturing with robots.",
  "End childhood hunger. Guarantee clean water, clean air, reliable electricity and sanitation.",
  // World Bank Bangladesh Development Update (2026): 21.4% below the national
  // poverty line in 2025, about 37 million people.
  "Lift 37 million Bangladeshis out of poverty: invest in AI-native companies that put them to work and lay the foundation for a services economy.",
  "Write the playbook for ensuring AGI benefits all of humanity.",
] as const;

function WhereThisGoes() {
  return (
    <Slide id="vision">
      <ul className="vision">
        {VISION.map((point) => (
          <li key={point}>{point}</li>
        ))}
      </ul>
    </Slide>
  );
}

// [Bracketed] text is a placeholder until the round is set.
const ASK_TERMS = [
  { term: "Raising", value: "[amount] at [valuation]" },
  { term: "Use of funds", value: "[collector pay · sensors · sales]" },
  { term: "Milestones", value: "[ARR and collectors by next round]" },
] as const;

function Ask() {
  return (
    <Slide id="ask">
      <div className="ask">
        <p className="pitch-lead">
          We expect this to be a winner-take-all market. We aim to move fast and capture the best
          talent, investing rapidly to pay above market.
        </p>
        <dl className="ask-terms">
          {ASK_TERMS.map((t) => (
            <div key={t.term}>
              <dt>{t.term}</dt>
              <dd>
                <Fill text={t.value} />
              </dd>
            </div>
          ))}
        </dl>
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
    logoAt: { dx: -7, dy: -6 },
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
    name: "Wirestock",
    // Mar 2019; pivoted from stock distribution to AI training data in 2023.
    founded: "2019-03",
    logo: "/deck/logos/wirestock.png",
    logoAt: { dx: 13, dy: -4 },
    points: [
      { date: "2022-01", value: 2.3e6 },
      { date: "2026-05", value: 26e6 },
    ],
  },
  {
    name: "Origin Lab",
    // Undisclosed (one directory says 2022, unconfirmed); seed announced May 2026.
    founded: "2025-01",
    logo: "/deck/logos/originlab.png",
    logoAt: { dx: 13, dy: 4 },
    points: [{ date: "2026-05", value: 8e6 }],
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
    label: "EIF, Wirestock raises $2.3M (Jan 2022)",
    url: "https://blog.eif.am/img-grantee-wirestock-raises-usd-2-3m-in-funding-round/",
  },
  {
    label: "TechCrunch, Wirestock raises $23M Series A (May 2026)",
    url: "https://techcrunch.com/2026/05/14/wirestock-raises-23m-to-supply-multi-modal-data-to-ai-labs/",
  },
  {
    label: "TechCrunch, Origin Lab raises $8M seed (May 2026)",
    url: "https://techcrunch.com/2026/05/13/origin-lab-raises-8m-to-help-video-game-companies-sell-data-to-world-model-builders/",
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
          {/* Largest first-to-latest multiple charted: micro1, $7M (Jan 2025) to $500M (Aug 2026). */}
          <li>Seller revenue grew up to 71× between 2025 and 2026.</li>
          {/* Gartner, May 2026 "AI Data" spend: $0.83B (2025) → $3.1B (2026) → $6.5B (2027). */}
          <li>Buyer spending is expected to grow 7.8× between 2025 and 2027.</li>
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

// [Bracketed] text is a placeholder until data collection starts.
const MOATS = [
  {
    name: "Already on the ground",
    points: [
      "Bangladeshi entity: [name, registered date]",
      "[N] acres under agreement in Khulna",
      "[N] hours collected · [N] contributors",
    ],
  },
  {
    name: "Relationships",
    points: [
      "Political and social connections in SF, Seattle, and Bangladesh",
      "Existing relationships at OpenAI: [DevDay 2026, contacts]",
    ],
  },
  {
    name: "Talent",
    points: [
      "We hire in and out of Bangladesh: a larger pool than competitors",
      "Above-market pay",
      "A positive mission",
    ],
  },
  {
    name: "Data nobody else has",
    points: [
      "Same fields, recorded every season: [N] acres × [N] seasons",
      "[N] buyers · [N] repeat orders",
    ],
  },
] as const;

// Renders [bracketed] runs as visible placeholders.
function Fill({ text }: { text: string }) {
  return text
    .split(/(\[[^\]]*\])/)
    .map((part, i) => (part.startsWith("[") ? <mark key={i}>{part}</mark> : part));
}

function Moats() {
  return (
    <Slide id="moats">
      <ul className="moats">
        {MOATS.map((m, i) => (
          <li key={m.name} style={{ borderColor: `var(--series-${(i % 3) + 1})` }}>
            <strong>{m.name}</strong>
            <ul>
              {m.points.map((pt) => (
                <li key={pt}>
                  <Fill text={pt} />
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ul>
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
  // A line, a bullet list, or a line led by an inline logo.
  bio: readonly (string | readonly string[] | { text: string; logo: Logo })[];
  logos: readonly Placed[];
};

const TEAM: readonly Member[] = [
  {
    name: "Shovon Hasan",
    role: "Founder, CEO",
    photo: "/deck/team/shovon.jpg",
    url: "https://x.com/anveio",
    bio: [
      "Modernizing ops at AWS EC2 & Bedrock",
      ["Capacity Reservations, UltraServers, Auto Scaling, Spot", "Mantle, Playground"],
      "Payments, Fraud, and Risk at Patreon.",
      "From Queens, New York. Born in Bangladesh to a landowning family.",
      { text: "DevDay 2026 Attendee", logo: { src: "/deck/logos/openai.svg", alt: "OpenAI" } },
    ],
    logos: [
      { src: "/deck/logos/aws.svg", alt: "AWS", x: 6, y: 18, h: 52 },
      { src: "/deck/logos/patreon-mark.svg", alt: "Patreon", x: 148, y: 4, h: 58 },
      { src: "/deck/logos/madhive-mark.png", alt: "MadHive", x: 214, y: 78, h: 50 },
      { src: "/deck/logos/emory-mark.svg", alt: "Emory University", x: 52, y: 92, h: 64 },
    ],
  },
  {
    name: "Dr. Tazia Sardar, DPT",
    role: "Advisor",
    photo: null,
    url: "https://www.linkedin.com/in/dr-tazia-sardar-dpt-2751b1a8/",
    bio: [],
    logos: [],
  },
  {
    name: "Dr. Md Abdullah Yousuf, DO",
    role: "Advisor",
    photo: null,
    url: null,
    bio: [],
    logos: [],
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

function WhoWeAre() {
  return (
    <Slide id="who">
      <ul className="team">
        {TEAM.map((person) => (
          <li key={person.name}>
            <div className="team-who">
              {person.photo ? (
                <img className="avatar" src={person.photo} alt={person.name} />
              ) : (
                <BlankAvatar />
              )}
              <strong>
                {person.url ? (
                  <a href={person.url} target="_blank" rel="noreferrer">
                    {person.name}
                  </a>
                ) : (
                  person.name
                )}
              </strong>
              <span className="team-role">{person.role}</span>
            </div>
            <div className="team-bio">
              {person.bio.map((line) =>
                typeof line === "string" ? (
                  <p key={line}>{line}</p>
                ) : "text" in line ? (
                  <p key={line.text} className="team-badge">
                    <img src={line.logo.src} alt={line.logo.alt} />
                    {line.text}
                  </p>
                ) : (
                  <ul key={line.join()}>
                    {line.map((point) => (
                      <li key={point}>{point}</li>
                    ))}
                  </ul>
                ),
              )}
            </div>
            <div className="team-logos">
              {person.logos.map((logo) => (
                <img
                  key={logo.src}
                  className="team-logo"
                  src={logo.src}
                  alt={logo.alt}
                  style={{ left: logo.x, top: logo.y, height: logo.h }}
                />
              ))}
            </div>
          </li>
        ))}
      </ul>
    </Slide>
  );
}

const CANVAS = { width: 1280, height: 720 } as const;
const GUTTER = 16;

// Scales the fixed 16:9 canvas so one whole slide fits the window.
function useDeckScale() {
  useEffect(() => {
    const fit = () => {
      const scale = Math.min(
        (window.innerWidth - 2 * GUTTER) / CANVAS.width,
        (window.innerHeight - 2 * GUTTER) / CANVAS.height,
      );
      document.documentElement.style.setProperty("--deck-scale", String(Math.max(0.1, scale)));
    };
    fit();
    window.addEventListener("resize", fit);
    return () => window.removeEventListener("resize", fit);
  }, []);
}

function Sources() {
  const groups = [
    { name: "Market", items: MARKET_SOURCES },
    { name: "Photos", items: SAMPLE_SOURCES },
    { name: "Logos", items: LOGO_SOURCES },
  ] as const;
  return (
    <Slide id="sources">
      <div className="sources">
        {groups.map((g) => (
          <section key={g.name}>
            <h2 className="sources-head">{g.name}</h2>
            <ol>
              {g.items.map((src) => (
                <li key={src.url}>
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

// Arrow and page keys step one slide at a time.
function useKeyboardNav() {
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      if (target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA")) return;
      const delta = {
        ArrowDown: 1,
        PageDown: 1,
        ArrowRight: 1,
        ArrowUp: -1,
        PageUp: -1,
        ArrowLeft: -1,
      }[event.key];
      if (!delta) return;
      event.preventDefault();
      const tops = SLIDES.map(
        (sl) => document.getElementById(sl.id)?.getBoundingClientRect().top ?? 0,
      );
      const current = tops.reduce(
        (best, top, i) => (Math.abs(top) < Math.abs(tops[best]!) ? i : best),
        0,
      );
      const next = Math.max(0, Math.min(SLIDES.length - 1, current + delta));
      document
        .getElementById(SLIDES[next]!.id)
        ?.scrollIntoView({ behavior: "smooth", block: "start" });
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
}

export function Deck() {
  useDeckScale();
  useKeyboardNav();
  return (
    <>
      <main className="deck">
        <Title />
        <WhoWeAre />
        <Cover />
        <Moats />
        <Market />
        <WhyBangladesh />
        <Economics />
        <PathToBillion />
        <WhereThisGoes />
        <Ask />
        <Sources />
      </main>
    </>
  );
}

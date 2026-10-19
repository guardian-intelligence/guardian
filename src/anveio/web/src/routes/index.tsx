import { createFileRoute } from "@tanstack/react-router";
import { WINGS_CROPPED_VIEWBOX, WINGS_PATH_D } from "@guardian/brand/components/wings";
import savedScale from "~/home/type-scale.json";
import { fontGateScript } from "~/home/fontGate";
import {
  firstPaintFonts,
  googleStylesheet,
  parseScale,
  preloadFiles,
  typeCss,
} from "~/home/typeScale";
import "~/styles/home.css";

// The type scale /design edits; its CSS is rendered with the page.
const SCALE = parseScale(savedScale);
const TYPE_CSS = typeCss(SCALE);
const GOOGLE_FONTS = googleStylesheet(SCALE);
const FONT_GATE = fontGateScript(firstPaintFonts(SCALE));

export const Route = createFileRoute("/")({
  component: Home,
  head: () => ({
    meta: [
      { title: "Anveio — The Data Company of Bangladesh" },
      // The hero's top edge (the white veil over the creative's sky), for
      // browsers that tint their toolbar from theme-color; Safari 26 reads
      // the root background instead (home.css).
      { name: "theme-color", content: "#f2f5f7" },
      {
        name: "description",
        content:
          "We provide state-of-the-art Reinforcement Learning environments to benchmark artificial intelligence on real-world tasks.",
      },
    ],
    links: [
      ...preloadFiles(SCALE).map((file) => ({
        rel: "preload",
        href: `/deck/fonts/${file}`,
        as: "font",
        type: "font/woff2",
        crossOrigin: "anonymous" as const,
      })),
      ...(GOOGLE_FONTS
        ? [
            {
              rel: "preconnect",
              href: "https://fonts.gstatic.com",
              crossOrigin: "anonymous" as const,
            },
            { rel: "stylesheet", href: GOOGLE_FONTS },
          ]
        : []),
    ],
  }),
});

const CONTACT = "mailto:shovon@anveio.com";

// Keeps a text's last two words on one line, so no block ever ends with
// one word (or the tail of a hyphenated one) alone; text-wrap: pretty isn't
// enough in Chromium or older Safari.
function tie(text: string) {
  const at = text.lastIndexOf(" ", text.lastIndexOf(" ") - 1);
  if (at < 0) return text;
  return (
    <>
      {text.slice(0, at + 1)}
      <span className="hp-tie">{text.slice(at + 1)}</span>
    </>
  );
}

// The company's line, justified to one width in three lines, as on the
// deck's title slide (home.css). "The Data" is spread letter by letter,
// its word space one more even slot; Bangladesh is set letter by letter
// too, so it can track out when another line is wider.
const TITLE = [
  { line: "The Data", parts: "The\u00a0Data".split(""), spread: true },
  { line: "Company of", parts: ["Company", "of"], spread: false },
  { line: "Bangladesh", parts: "Bangladesh".split(""), spread: true },
] as const;

// The deck's title-slide lockup, small: the wings beside ANVEIO in Archivo
// Semi Expanded caps.
function Lockup({ className }: { className?: string }) {
  return (
    <a href="/" className={`hp-lockup ${className ?? ""}`} aria-label="Anveio home">
      <svg viewBox={WINGS_CROPPED_VIEWBOX} aria-hidden="true">
        <path d={WINGS_PATH_D} />
      </svg>
      <span aria-hidden="true">Anveio</span>
    </a>
  );
}

function Creative({ className }: { className: string }) {
  return (
    // WebM (VP9) first; H.264 for iPhones whose Safari can't play VP9.
    <video
      className={className}
      poster="/home/farm-poster.jpg"
      autoPlay
      muted
      loop
      playsInline
      preload="auto"
      aria-hidden="true"
    >
      <source src="/home/farm.webm" type="video/webm" />
      <source src="/home/farm.mp4" type="video/mp4" />
    </video>
  );
}

// Fading vertical guides, three nested pairs, after hud.ai's hero.
function Guides() {
  return (
    <div className="hp-guides" aria-hidden="true">
      <span />
      <span />
      <span />
    </div>
  );
}

const STREAMS = [
  {
    id: "drone",
    tone: "green",
    title: "Drone video",
    body: "Low passes over real farms: trellised crops, ponds and khals, mapped into a digital twin of every plot.",
    images: ["drone-1.jpg", "drone-2.jpg", "drone-3.jpg"],
  },
  {
    id: "egocentric",
    tone: "pink",
    title: "Egocentric video",
    body: "First-person footage of skilled hands at work, recorded where the work actually happens.",
    images: ["egocentric-1.jpg", "egocentric-2.jpg"],
  },
  {
    id: "driving",
    tone: "peach",
    title: "Dashcam",
    body: "Driving on some of the densest roads in the world.",
    images: ["driving-1.jpg"],
  },
] as const;

const WHY = [
  {
    title: "Density",
    body: "The highest population density of any large country, and a US-friendly government.",
  },
  {
    title: "Land",
    body: "Rich in farms and fertile, low-cost land.",
  },
  {
    title: "Moat",
    body: "The first company to deploy data collection at scale wins. A small economy is winner-take-all.",
  },
  {
    title: "Partner",
    body: "The default data partner for buyers of high-quality physical-world data in the region.",
  },
] as const;

const FAQ = [
  {
    q: "What is Anveio?",
    a: "A data company in Bangladesh. We record the physical world, starting with farms, and turn it into training and evaluation data for AI labs.",
  },
  {
    q: "What is the RL environment?",
    a: "A real-time strategy game built from our drone scans of real farms. A model plays a rollout as the farm's operator and is graded on the outcome.",
  },
  {
    q: "How do I play?",
    a: "Soon you'll sign in with your ChatGPT or Claude account to watch a rollout inside the environment and see its grade.",
  },
  {
    q: "How do I get in touch?",
    a: "Leave a message with our agent, or email shovon@anveio.com.",
  },
] as const;

function PlayNow({ className }: { className?: string }) {
  return (
    <button type="button" className={`hp-btn hp-btn-dark ${className ?? ""}`} disabled>
      Play Now
      <span className="hp-soon">Coming soon</span>
    </button>
  );
}

function Home() {
  return (
    <div className="hp">
      <style id="hp-type">{TYPE_CSS}</style>
      {/* Holds the entrance until the fonts load (src/home/fontGate.ts). */}
      <script dangerouslySetInnerHTML={{ __html: FONT_GATE }} />
      <header className="hp-nav">
        <div className="hp-nav-inner rise rise-fade rise-1">
          <Lockup />
          <nav className="hp-nav-links" aria-label="Sections">
            <a href="#data">Data</a>
            <a href="#why">Why Bangladesh</a>
            <a href="/deck">
              Deck <Arrow />
            </a>
          </nav>
          <div className="hp-nav-end">
            <a className="hp-nav-link" href={CONTACT}>
              Contact
            </a>
            <a className="hp-btn hp-btn-dark hp-btn-sm" href="/agent">
              Leave a message
            </a>
          </div>
        </div>
      </header>

      <section className="hp-hero">
        <div className="hp-hero-media">
          <Creative className="hp-hero-video" />
        </div>
        <div className="hp-hero-veil-top" />
        <div className="hp-hero-veil-bottom" />
        <Guides />
        <div className="hp-hero-copy">
          <h1 className="hp-title" aria-label="The Data Company of Bangladesh">
            {TITLE.map(({ line, parts, spread }, n) => (
              <span
                key={line}
                className={`rise rise-${n + 1} ${spread ? "hp-spread" : ""}`}
                aria-hidden="true"
              >
                {parts.map((part, i) => (
                  // Letters repeat, so position is the key.
                  <span key={i}>{part}</span>
                ))}
              </span>
            ))}
          </h1>
          <p className="hp-sub rise rise-4">
            {tie(
              "We provide state-of-the-art Reinforcement Learning environments to benchmark artificial intelligence on real-world tasks.",
            )}
          </p>
          <div className="hp-ctas rise rise-5">
            <PlayNow />
          </div>
        </div>
      </section>
      <div className="hp-seam" />

      <section className="hp-infra">
        <div className="hp-infra-media" aria-hidden="true">
          <Creative className="hp-infra-video" />
          <div className="hp-infra-dim" />
        </div>
        <div className="hp-infra-copy">
          <h2 className="rise rise-1">{tie("Grounding AI in the physical world.")}</h2>
          <p>
            <span className="rise rise-2">
              {tie("The last generation of data was scraped from screens.")}
            </span>
            <span className="rise rise-3">
              {tie("The next is recorded in fields, homes and streets.")}
            </span>
          </p>
        </div>
      </section>

      <section className="hp-bento" id="data">
        <div className="hp-bento-grid">
          {STREAMS.map((s) => (
            <article key={s.id} className={`hp-cell hp-cell-${s.id} tone-${s.tone}`}>
              <div className="hp-cell-text">
                <h3>{s.title}</h3>
                <p>{tie(s.body)}</p>
              </div>
              <div className="hp-cell-media">
                {s.images.map((img) => (
                  <img key={img} src={`/deck/${img}`} alt="" loading="lazy" />
                ))}
              </div>
            </article>
          ))}
          <span className="hp-cross" style={{ left: 0, top: "100%" }} />
          <span className="hp-cross" style={{ left: "66.666%", top: "100%" }} />
          <span className="hp-cross" style={{ left: "100%", top: "100%" }} />
        </div>
      </section>

      <section className="hp-section" id="why">
        <header className="hp-section-head">
          <h2 className="hp-serif">Why Bangladesh</h2>
          <p>{tie("Physical-world data, from the country built to supply it.")}</p>
        </header>
        <div className="hp-why">
          {WHY.map((w, i) => (
            <div key={w.title} className="hp-why-cell">
              <span className="hp-badge" aria-hidden="true">
                {i + 1}
              </span>
              <h3>{w.title}</h3>
              <p>{tie(w.body)}</p>
            </div>
          ))}
          <span className="hp-cross" style={{ left: 0, top: 0 }} />
          <span className="hp-cross" style={{ left: "100%", top: 0 }} />
          <span className="hp-cross" style={{ left: 0, top: "100%" }} />
          <span className="hp-cross" style={{ left: "100%", top: "100%" }} />
        </div>
      </section>

      <section className="hp-section hp-faq-section" id="faq">
        <header className="hp-section-head">
          <h2 className="hp-serif">Frequently asked questions</h2>
        </header>
        <div className="hp-faq">
          {FAQ.map((f) => (
            <details key={f.q}>
              <summary>
                {f.q}
                <Plus />
              </summary>
              <p>{tie(f.a)}</p>
            </details>
          ))}
        </div>
      </section>

      <footer className="hp-foot" id="play">
        <div className="hp-foot-cta">
          <div>
            <h2>{tie("Watch your model farm.")}</h2>
            <p>{tie("The environment opens soon. Leave a message to hear first.")}</p>
          </div>
          <div className="hp-foot-btns">
            <PlayNow className="hp-btn-light" />
            <a className="hp-btn hp-btn-plum" href="/agent">
              Leave a message
            </a>
          </div>
        </div>
        <div className="hp-foot-grid">
          <div className="hp-foot-brand">
            <Lockup className="on-ink" />
            <p>{tie("The Data Company of Bangladesh.")}</p>
          </div>
          <div className="hp-foot-col">
            <h4>Company</h4>
            <a href="/deck">
              Deck <Arrow />
            </a>
            <a href="/agent">Agent</a>
            <a href={CONTACT}>Contact</a>
          </div>
          <div className="hp-foot-col">
            <h4>Data</h4>
            <a href="#data">Drone</a>
            <a href="#data">Egocentric</a>
            <a href="#data">Dashcam</a>
          </div>
          <div className="hp-foot-col">
            <h4>Legal</h4>
            <a href="/privacy">Privacy</a>
            <span className="hp-foot-copy">© 2026 Anveio Foundation</span>
          </div>
        </div>
        <div className="hp-foot-media" aria-hidden="true">
          <Creative className="hp-foot-video" />
          <div className="hp-foot-lines">
            <span />
            <span />
            <span />
          </div>
        </div>
      </footer>
    </div>
  );
}

function Arrow() {
  return (
    <svg className="hp-arrow" viewBox="0 0 12 12" aria-hidden="true">
      <path d="M3.5 8.5l5-5M4.5 3.5h4v4" fill="none" stroke="currentColor" strokeWidth="1.2" />
    </svg>
  );
}

function Plus() {
  return (
    <svg className="hp-plus" viewBox="0 0 16 16" aria-hidden="true">
      <path d="M8 3v10M3 8h10" fill="none" stroke="currentColor" strokeWidth="1.3" />
    </svg>
  );
}

import { createFileRoute } from "@tanstack/react-router";
import preview from "~/deck/social-preview.json";
import { pageHead, Site } from "~/site/Site";

const head = pageHead({
  path: "/",
  title: "Anveio — The Data Company of Bangladesh",
  description:
    "Anveio pays farms in Bangladesh to record drone video and turns each flight into a digital twin of the farm.",
});

export const Route = createFileRoute("/")({
  component: Home,
  head: () => ({
    ...head,
    meta: [
      ...head.meta,
      // The deck's title card doubles as the company's link preview.
      { property: "og:image", content: `https://anveio.com${preview.image}` },
      { property: "og:image:width", content: String(preview.width) },
      { property: "og:image:height", content: String(preview.height) },
    ],
  }),
});

// What a flight gives a farm, in the order it happens.
const WORK = [
  {
    title: "Record",
    body: "We pay farms in Bangladesh to record drone video of their land.",
  },
  {
    title: "Map",
    body: "A first pass over a plot maps it into a digital twin: crop beds and trellises, drainage lines, pipes, wiring, houses and sheds. Each flight after that updates the twin with what changed, from a rusting roof to a bean plant’s new pods.",
  },
  {
    title: "Ground truth",
    body: "Footage shows what a farm looks like. The farm’s own records, of what was picked, what was sprayed and which pests turned up, show what happened after each decision. Together they make data an AI model can be tested against.",
  },
  {
    title: "License",
    body: "We enrich and format the footage and license it to AI companies.",
  },
] as const;

export function Home() {
  return (
    <Site>
      <section className="site-hero" aria-labelledby="hero-title">
        <h1 id="hero-title" className="site-title">
          The Data Company of Bangladesh
        </h1>
        <p className="site-lead">
          Fly a drone over a farm often enough and the footage becomes a digital twin of it: every
          field, drain, pipe and plant, and how each one changes.
        </p>
      </section>

      <Stage />

      <section className="site-section" aria-labelledby="work-title">
        <h2 id="work-title" className="site-h2">
          What we do
        </h2>
        <div className="site-work">
          {WORK.map((item) => (
            <div key={item.title}>
              <h3 className="site-h3">{item.title}</h3>
              <p>{item.body}</p>
            </div>
          ))}
        </div>
      </section>

      <section className="site-section" aria-labelledby="mission-title">
        <h2 id="mission-title" className="site-h2">
          Our mission
        </h2>
        <p className="site-mission">Deploy AI in Bangladesh for the benefit of all people.</p>
      </section>
    </Site>
  );
}

// The stage the digital-twin animation will play on, held at 16:9. Until it
// ships it shows the animation's first frame: the plot before the drone's
// first pass, a shadowed rectangle seen from above at an angle.
function Stage() {
  return (
    <figure className="site-stage">
      <svg viewBox="0 0 1600 900" aria-hidden="true">
        <defs>
          <linearGradient id="plot-shade" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" stopColor="#e9eeeb" stopOpacity="0.02" />
            <stop offset="1" stopColor="#e9eeeb" stopOpacity="0.07" />
          </linearGradient>
        </defs>
        <path d="M520 300 H1080 L1300 700 H300 Z" fill="url(#plot-shade)" />
        <path
          d="M520 300 H1080 L1300 700 H300 Z"
          fill="none"
          stroke="#e9eeeb"
          strokeOpacity="0.16"
          strokeWidth="1.5"
        />
      </svg>
      <figcaption>An unmapped plot, before the drone’s first pass.</figcaption>
    </figure>
  );
}

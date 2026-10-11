// Who buys this data: each company's full logo, its wordmark wherever one
// exists (Figure and Physical Intelligence use their marks alone), scattered
// loosely across a band as if dropped onto the slide. Marks belong to their
// owners and stand for illustrative targets.
//
// Heights even out the logos' ink area (about 3,000 px²), so a long wordmark
// and a square mark read alike; (x, y) is each logo's centre in the band.
const BAND = { w: 1100, h: 124 };

const LOGOS = [
  { name: "OpenAI", src: "openai-wordmark.svg", h: 28, x: 80, y: 28 },
  { name: "Protege", src: "protege-wordmark.svg", h: 26, x: 232, y: 88 },
  { name: "Anthropic", src: "anthropic-wordmark.svg", h: 18, x: 402, y: 34 },
  { name: "Figure", src: "figure-mark.svg", h: 40, x: 338, y: 96 },
  { name: "Troveo", src: "troveo-wordmark.svg", h: 21, x: 560, y: 86 },
  { name: "Google DeepMind", src: "deepmind-wordmark.png", h: 26, x: 700, y: 26 },
  { name: "Physical Intelligence", src: "physicalintelligence-mark.png", h: 36, x: 750, y: 92 },
  { name: "Wirestock", src: "wirestock-wordmark.svg", h: 23, x: 862, y: 80 },
  { name: "Skild AI", src: "skild-wordmark.svg", h: 26, x: 990, y: 30 },
  { name: "Kled", src: "kled-wordmark.svg", h: 20, x: 1036, y: 92 },
] as const;

export function Buyers() {
  return (
    <section className="buyers">
      <h3 className="buyers-heading">Buyers</h3>
      <div className="buyers-band" style={{ width: BAND.w, height: BAND.h }}>
        {LOGOS.map((l) => (
          <img
            key={l.name}
            src={`/deck/logos/${l.src}`}
            alt={l.name}
            style={{ left: l.x, top: l.y, height: l.h }}
          />
        ))}
      </div>
    </section>
  );
}

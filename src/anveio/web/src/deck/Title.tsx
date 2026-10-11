import { WINGS_CROPPED_VIEWBOX, WINGS_PATH_D } from "@guardian/brand/components/wings";
import { Lockup } from "./Brand";

// The company's line, justified to the lockup's width in three lines.
const LINE = [["The", "Data"], ["Company", "of"], ["Bangladesh"]] as const;

// The opening slide, set full-bleed in ink: the Anveio lockup at hero size,
// redrawn for the scale (lighter, tighter and closer than the footer's), and
// beneath it the company's line in condensed serif caps, after the Wall
// Street Journal's masthead.
export function Title() {
  return (
    <section id="title" className="slide" aria-label="Anveio">
      <div className="slide-frame">
        <div className="slide-inner title-slide">
          <div className="title-center">
            <div className="title-stack">
              <h1 className="title-hero" aria-label="Anveio">
                <svg viewBox={WINGS_CROPPED_VIEWBOX} aria-hidden="true">
                  <path d={WINGS_PATH_D} />
                </svg>
                <span aria-hidden="true">Anveio</span>
              </h1>
              <div className="title-below">
                <hr className="title-rule" />
                <p className="title-line" aria-label="The Data Company of Bangladesh">
                  {LINE.map((words) => (
                    <span key={words.join(" ")} aria-hidden="true">
                      {words.map((word) => (
                        <span key={word}>{word}</span>
                      ))}
                    </span>
                  ))}
                </p>
              </div>
            </div>
          </div>
          <footer className="slide-foot">
            <Lockup onInk />
            <span>Investor preview · October 2026</span>
          </footer>
        </div>
      </div>
    </section>
  );
}

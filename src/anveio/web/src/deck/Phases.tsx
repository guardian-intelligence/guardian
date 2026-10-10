import type { ReactNode } from "react";
import { Fill } from "./Fill";

// [Bracketed] text is a placeholder until the numbers are set.

export type Gate = { criterion: string; actual?: string };

export type PhaseCopy = {
  window: string;
  headline: string;
  bullets: readonly string[];
  law: string;
  gates: readonly Gate[];
  // Where the gate strip leads.
  next: string;
};

// One phase slide body: headline, bullets, the phase's growth mechanism in
// one line, then the measurable gate to the next phase.
export function PhaseBody({ copy, chart }: { copy: PhaseCopy; chart?: ReactNode }) {
  return (
    <div className="phase">
      <p className="phase-lead">
        <span className="phase-window">{copy.window}</span>
        <span>
          <Fill text={copy.headline} />
        </span>
      </p>
      <div className="phase-main">
        <ul className="phase-points">
          {copy.bullets.map((b) => (
            <li key={b}>
              <Fill text={b} />
            </li>
          ))}
        </ul>
        {chart && <div className="phase-chart">{chart}</div>}
      </div>
      <p className="phase-law">
        <span>Scaling law</span>
        <span className="phase-law-text">
          <Fill text={copy.law} />
        </span>
      </p>
      <div className="phase-gates">
        <span className="phase-gates-head">{copy.next}</span>
        <ul>
          {copy.gates.map((g) => (
            <li key={g.criterion}>
              <span>
                <Fill text={g.criterion} />
              </span>
              {g.actual && (
                <span className="phase-actual">
                  <Fill text={g.actual} />
                </span>
              )}
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}

export const PHASE_2: PhaseCopy = {
  window: "[From —]",
  headline: "Scale to more farms: the same stack on every plot.",
  bullets: [
    "Protege-style network: partner farms share sensor-synced data for a revenue share.",
    "The same stack on every plot: video, sensors, labels and evals linked by plot and season.",
    "Differences between plots are the product: real-time effects of small changes, across the country.",
    "Land-light and within the law: farmers cultivate their own land; we own only the sub-8 ha model farm.",
  ],
  law: "Revenue = farms × instrumented hrs/yr × [$/hr] + evals across farms.",
  next: "Milestones",
  gates: [
    { criterion: "≥ [N] partner farms" },
    { criterion: "Farm-data price ≥ [$/hr] at volume" },
    { criterion: "Consent and data-sharing template accepted by every partner farm" },
  ],
};

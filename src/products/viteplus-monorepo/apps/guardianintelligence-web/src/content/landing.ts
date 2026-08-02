// Landing copy lives in content/, not in JSX. The route is structural —
// components reference `landing.hero`, `landing.mission[0]`, etc. — so a forker
// rewrites this file with their coding agent and the routes keep working.
//
// Voice rules in brand/voice.md apply.

export const landing = {
  kicker: "Accepting Applications for Fall 2026",
  hero: "Guardian",
  lede: "We build software libraries for companies working on AI alignment — for zero-cost. The only catch: the software will be free and open‑source.",
} as const;

export type LandingContent = typeof landing;

// Landing copy lives in content/, not in JSX. The route is structural —
// components reference `landing.hero`, `landing.mission[0]`, etc. — so a forker
// rewrites this file with their coding agent and the routes keep working.
//
// Voice rules in brand/voice.md apply.

export const landing = {
  kicker: "Seattle, WA · Est. 2026",
  hero: "GUARDIAN",
  lede: "We build free, open-source agent-native software to democratize AI alignment, towards a humanist future.",
} as const;

export type LandingContent = typeof landing;

// Landing copy lives in content/, not in JSX. The route is structural —
// components reference `landing.hero`, `landing.mission[0]`, etc. — so a forker
// rewrites this file with their coding agent and the routes keep working.
//
// Voice rules in brand/voice.md apply.

export const landing = {
  kicker: "Forward-deployed intelligence",
  hero: "Request any software",
  lede: "Tell us what your company needs. We build the strongest reusable ideas in the open, or privately under an enterprise contract.",
  requestPlaceholder:
    "Describe the software you wish existed, the problem it should solve, and what success looks like.",
  openSourceNote:
    "Open-source requests are reviewed at no cost. Private builds require an enterprise contract.",
} as const;

export type LandingContent = typeof landing;

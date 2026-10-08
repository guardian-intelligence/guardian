// Copy and timeline data. Rumi's lines and Shovon's note are verbatim; don't paraphrase.

export const NOTE =
  "Hi, feel free to schedule time with me by talking to my personal assistant down there.";

export const LINES = {
  greet: "Hi, I'm Rumi. I can take a message or schedule time with you and Shovon.",
  signin: "Before I listen, who's calling? Sign in so Shovon knows it's you.",
  listening: "Go ahead, I'm listening.",
  think1: "Friday at 2:30 looks open…",
  booked: "Friday at 2:30 it is. I've sent the invitation to you both.",
  think2: "Nothing earlier on Friday. Thursday at 2:30 is open…",
  rebooked: "Moved to Thursday at 2:30, well clear of your piano lesson.",
  novoice:
    "I couldn't hear a voice in that one. Messages to Shovon need to be spoken, so try again somewhere a little quieter.",
  host: "Samantha moved your chat to Thursday at 2:30. Her second message says why.",
} as const;

export const STAGES = [
  "arrive",
  "signin",
  "listening",
  "thinking",
  "booked",
  "return",
  "updating",
  "thinking2",
  "rebooked",
  "host",
  "novoice",
] as const;
export type Stage = (typeof STAGES)[number];

export const STAGE_LINE = {
  arrive: LINES.greet,
  signin: LINES.signin,
  listening: LINES.listening,
  thinking: LINES.think1,
  booked: LINES.booked,
  return: LINES.greet,
  updating: LINES.listening,
  thinking2: LINES.think2,
  rebooked: LINES.rebooked,
  novoice: LINES.novoice,
  host: LINES.host,
} as const satisfies Record<Stage, string>;

export type EntryKind = "event" | "post" | "code";
export type Entry = {
  readonly kind: EntryKind;
  readonly date: string;
  readonly text: string;
  readonly url: string;
};

// Shovon's office timeline, newest first. Add events, posts and code here.
export const ENTRIES: readonly Entry[] = [
  {
    kind: "code",
    date: "2026-09-30",
    text: "Drop the Postflight host manifest and reconciler",
    url: "https://github.com/guardian-intelligence/guardian/pull/1582",
  },
  {
    kind: "code",
    date: "2026-09-30",
    text: "Route operator cluster access through Cloudflare Access",
    url: "https://github.com/guardian-intelligence/guardian/pull/1581",
  },
  {
    kind: "code",
    date: "2026-09-28",
    text: "Trim the pnpm workspace-layout paragraph from AGENTS.md",
    url: "https://github.com/guardian-intelligence/guardian/pull/1580",
  },
  {
    kind: "code",
    date: "2026-09-28",
    text: "Read Postgres backup freshness from the barman-cloud plugin",
    url: "https://github.com/guardian-intelligence/guardian/pull/1579",
  },
  {
    kind: "code",
    date: "2026-09-28",
    text: "Apply the contact@ Email Routing resources and resume the token minter",
    url: "https://github.com/guardian-intelligence/guardian/pull/1578",
  },
  {
    kind: "code",
    date: "2026-09-28",
    text: "Forward contact@guardianintelligence.org to the founder inbox",
    url: "https://github.com/guardian-intelligence/guardian/pull/1577",
  },
  {
    kind: "code",
    date: "2026-09-27",
    text: "Roll the barman-cloud plugin via podLabels, which its chart renders",
    url: "https://github.com/guardian-intelligence/guardian/pull/1576",
  },
  {
    kind: "event",
    date: "2026-09-29",
    text: "Attended OpenAI DevDay in San Francisco",
    url: "https://devday.openai.com/",
  },
  {
    kind: "post",
    date: "2026-09-28",
    text: "if you see a guy in a blue AWS hoodie on DevDay that's me",
    url: "https://x.com/anveio/status/2104737227955945939",
  },
  {
    kind: "post",
    date: "2026-09-23",
    text: "Earth is the teacher model and we are the student model.",
    url: "https://x.com/anveio/status/2102687871773909360",
  },
  {
    kind: "post",
    date: "2026-09-20",
    text: "I love this post. I wish I had written it",
    url: "https://x.com/anveio/status/2101790899143618862",
  },
  {
    kind: "post",
    date: "2026-09-18",
    text: "Asked Astra to fix my Philips Hue -> Apple Home integration and told it to use my phone camera to check if its changes worked.",
    url: "https://x.com/anveio/status/2100845826826449149",
  },
  {
    kind: "post",
    date: "2026-09-12",
    text: "I turned Dario's post into an audiobook. Enjoy",
    url: "https://x.com/anveio/status/2098897643137302677",
  },
  {
    kind: "post",
    date: "2026-09-12",
    text: "A sign we’re in the good timeline.",
    url: "https://x.com/anveio/status/2098892671033241991",
  },
];

// Kinds that collapse to their newest item plus "N more recent …".
export const GROUP_NOUN = { post: "tweets", code: "commits" } as const satisfies Partial<
  Record<EntryKind, string>
>;
